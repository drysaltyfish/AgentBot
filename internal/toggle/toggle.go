// Package toggle 实现 F-19 功能开关中间件：按 (插件名, 群号) 维度记录
// “本群是否启用该插件”，并提供内存与文件两种 Store 实现。
//
// 挂载方式为 router 的 pre 钩子：开关关闭时 Rule 返回 false 拒绝该路由。
// Toggle 不持有任何包级可变状态，所有状态都在实例上。
package toggle

import (
	"errors"
	"fmt"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/router"
)

// 哨兵错误。Store.Get 的签名不含 error，因此读取失败只能由实现以 panic
// 表达（见 storeGet）；写入路径直接使用这些错误。
var (
	errUnknownPlugin = errors.New("unknown plugin")
	errNilStore      = errors.New("nil store")
)

// Key 是一个开关的定位键：插件名 + 群号。
type Key struct {
	Plugin  string
	GroupID int64
}

// Store 是开关状态的持久化抽象，实现必须并发安全。
type Store interface {
	// Get 读取键状态；found 为 false 表示该键从未设置过。
	Get(Key) (on bool, found bool)
	// Set 写入键状态；I/O 失败返回 error。
	Set(Key, bool) error
	// Delete 删除键，使其回落到 defaultOn；I/O 失败返回 error。
	Delete(Key) error
	// All 返回所有显式设置过的键（含关闭项），顺序无关。
	All() ([]Key, error)
}

// Toggle 是功能开关中间件。零值不可用，必须经 New 构造。
type Toggle struct {
	store     Store
	defaultOn bool
	warn      func(string)

	mu         sync.RWMutex
	registered map[string]struct{}
}

// New 构造 Toggle。defaultOn 决定从未设置过的键的状态；warn 为 nil 时
// 忽略告警，否则用于显式记录 Store 读失败等异常。
func New(store Store, defaultOn bool, warn func(string)) *Toggle {
	return &Toggle{
		store:      store,
		defaultOn:  defaultOn,
		warn:       warn,
		registered: make(map[string]struct{}),
	}
}

// Register 在启动期声明“存在的插件”。只有声明过的插件名才能通过 Validate，
// 从而避免开启一个并不存在的开关。空串与重复项被忽略。
func (t *Toggle) Register(plugins ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range plugins {
		if p == "" {
			continue
		}
		t.registered[p] = struct{}{}
	}
}

// Validate 校验插件名是否已在 Register 中声明；未声明返回错误。
func (t *Toggle) Validate(plugin string) error {
	if plugin == "" {
		return fmt.Errorf("toggle: empty plugin name: %w", errUnknownPlugin)
	}
	t.mu.RLock()
	_, ok := t.registered[plugin]
	t.mu.RUnlock()
	if !ok {
		return fmt.Errorf("toggle: plugin %q not registered: %w", plugin, errUnknownPlugin)
	}
	return nil
}

// IsOn 返回插件在指定群的状态。键从未设置时返回 defaultOn；Store 读失败
// 时按 defaultOn 处理并告警（可用性优先，但绝不静默）。
func (t *Toggle) IsOn(plugin string, groupID int64) bool {
	on, found := t.storeGet(Key{Plugin: plugin, GroupID: groupID})
	if !found {
		return t.defaultOn
	}
	return on
}

// Set 校验插件名后写入 Store；校验或写入失败均返回带 %w 的错误。
func (t *Toggle) Set(plugin string, groupID int64, on bool) error {
	if err := t.Validate(plugin); err != nil {
		return err
	}
	if t.store == nil {
		return fmt.Errorf("toggle: set plugin %q group %d: %w", plugin, groupID, errNilStore)
	}
	if err := t.store.Set(Key{Plugin: plugin, GroupID: groupID}, on); err != nil {
		return fmt.Errorf("toggle: set plugin %q group %d: %w", plugin, groupID, err)
	}
	return nil
}

// EnabledGroups 返回插件被显式开启的群号（升序），供管理面板展示。
// 读取 Store 失败时告警并返回 nil。
func (t *Toggle) EnabledGroups(plugin string) []int64 {
	if t.store == nil {
		t.warnf("toggle: nil store, enabled groups for plugin %q unavailable", plugin)
		return nil
	}
	keys, err := t.store.All()
	if err != nil {
		t.warnf("toggle: list keys for plugin %q failed: %v", plugin, err)
		return nil
	}
	out := make([]int64, 0, len(keys))
	for _, k := range keys {
		if k.Plugin != plugin {
			continue
		}
		if on, found := t.storeGet(k); found && on {
			out = append(out, k.GroupID)
		}
	}
	sortInt64(out)
	return out
}

// Rule 返回 pre 钩子：该插件在该群被显式关闭时拒绝路由。
//
// 私有会话（GroupID==0）、nil 上下文或 nil Event 一律放行；
// Store 读取失败按 defaultOn 处理并告警。
func (t *Toggle) Rule(plugin string) router.Rule {
	return func(c *router.Ctx) bool {
		if c == nil || c.Event == nil {
			return true
		}
		if c.Event.GroupID == 0 {
			return true
		}
		return t.IsOn(plugin, c.Event.GroupID)
	}
}

// storeGet 读取键并对实现的 panic 兜底。Store.Get 没有 error 返回，读取
// 失败只能由实现以 panic 表达；这里恢复后按“不存在”处理，由调用方回落到
// defaultOn，同时发出告警，避免静默降级。
func (t *Toggle) storeGet(k Key) (on, found bool) {
	if t.store == nil {
		t.warnf("toggle: nil store, plugin=%q group=%d; using defaultOn=%v", k.Plugin, k.GroupID, t.defaultOn)
		return t.defaultOn, false
	}
	defer func() {
		if r := recover(); r != nil {
			t.warnf("toggle: store read failed, plugin=%q group=%d: %v; using defaultOn=%v", k.Plugin, k.GroupID, r, t.defaultOn)
			on, found = t.defaultOn, false
		}
	}()
	return t.store.Get(k)
}

// warnf 在已注入告警回调时记录一条告警。
func (t *Toggle) warnf(format string, args ...any) {
	if t.warn == nil {
		return
	}
	t.warn(fmt.Sprintf(format, args...))
}

// sortInt64 对切片做原地升序排序。
func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
