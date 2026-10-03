package session

import (
	"errors"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

var (
	// ErrTempRoutesUnavailable 表示会话管理器没有配置临时路由表。
	ErrTempRoutesUnavailable = errors.New("temp routes are not available")
	// ErrAwaitNoMatch 表示 Await 没有可用的匹配条件（防止误吞全部消息）。
	ErrAwaitNoMatch = errors.New("await requires a match predicate or a non-empty name")
)

// DefaultAwaitTTL 是 Await 的默认超时（配合 ctx 使用）。
const DefaultAwaitTTL = 5 * time.Minute

// TempRoute 是挂在会话上的临时路由（F-15）。
//
// 语义：注册后**优先于常规路由**被匹配；Once 为 true 时触发一次即自动移除；
// TTL 到期后自动失效；会话被回收时一并清理。
type TempRoute struct {
	// Key 是所属会话。
	Key Key
	// Name 便于日志与排查。
	Name string
	// Once 为 true 时触发一次后自动移除。
	Once bool
	// TTL 为 0 表示不过期。
	TTL time.Duration
	// Match 决定事件是否命中；nil 表示该会话的任意事件都命中。
	Match func(*event.Event) bool
	// Deliver 收到命中事件；可为 nil（表示只拦截不投递）。
	Deliver func(*event.Event)
}

type tempEntry struct {
	route   TempRoute
	expires time.Time
}

// TempTable 管理会话级临时路由。
//
// 并发安全：注册/投递/移除可能来自读循环、工作池与会话回收三条路径。
type TempTable struct {
	mu    sync.Mutex
	byKey map[Key][]*tempEntry
	now   func() time.Time
}

// NewTempTable 构造临时路由表。
func NewTempTable() *TempTable {
	return &TempTable{byKey: map[Key][]*tempEntry{}, now: time.Now}
}

// WithClock 注入时间源（测试用）。
func (t *TempTable) WithClock(now func() time.Time) *TempTable {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now != nil {
		t.now = now
	}
	return t
}

// Register 注册一条临时路由，返回幂等的移除函数。
func (t *TempTable) Register(r TempRoute) func() {
	t.mu.Lock()
	e := &tempEntry{route: r}
	if r.TTL > 0 {
		e.expires = t.now().Add(r.TTL)
	}
	t.byKey[r.Key] = append(t.byKey[r.Key], e)
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { t.removeEntry(r.Key, e) })
	}
}

// Offer 把事件交给该会话的临时路由。
//
// 命中则消费并返回 true——调用方据此**不再走常规路由**（F-15 的优先级要求）。
// 过期的条目会在匹配过程中被顺手清理。
func (t *TempTable) Offer(key Key, ev *event.Event) bool {
	if ev == nil {
		return false
	}
	t.mu.Lock()
	entries := t.byKey[key]
	now := t.now()

	// 先清理过期项，保持有界。
	live := entries[:0]
	for _, e := range entries {
		if !e.expired(now) {
			live = append(live, e)
		}
	}
	t.byKey[key] = live

	hit := -1
	for i, e := range live {
		if e.route.Match == nil || e.route.Match(ev) {
			hit = i
			break
		}
	}
	if hit < 0 {
		t.mu.Unlock()
		return false
	}

	chosen := live[hit]
	if chosen.route.Once {
		t.byKey[key] = append(live[:hit], live[hit+1:]...)
	}
	t.mu.Unlock()

	if chosen.route.Deliver != nil {
		chosen.route.Deliver(ev)
	}
	return true
}

// RemoveKey 移除某会话的全部临时路由，返回移除数量（会话回收时调用）。
func (t *TempTable) RemoveKey(key Key) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.byKey[key])
	delete(t.byKey, key)
	return n
}

// Len 返回当前临时路由总数。
func (t *TempTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for _, entries := range t.byKey {
		total += len(entries)
	}
	return total
}

func (e *tempEntry) expired(now time.Time) bool {
	return !e.expires.IsZero() && now.After(e.expires)
}

func (t *TempTable) removeEntry(key Key, target *tempEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := t.byKey[key]
	for i, e := range entries {
		if e == target {
			t.byKey[key] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(t.byKey[key]) == 0 {
		delete(t.byKey, key)
	}
}
