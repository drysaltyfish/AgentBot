// Package router 实现实例化路由注册表、稳定优先级排序、快照匹配与三段钩子
// （FEATURES.md F-08 ~ F-14、F-81）。
package router

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// State 键常量。F-10 要求键名集中定义，禁止散落字符串字面量。
const (
	StateKeyCommand    = "command"
	StateKeyArgs       = "args"
	StateKeyRegexMatch = "regex_match"
	StateKeyImageURLs  = "image_urls"
	StateKeyReplyID    = "reply_id"
	// StateKeyKeepPrefix 是保留键前缀：以它开头的键在切换到下一条路由时不被清理。
	StateKeyKeepPrefix = "__keep__"
)

// State 是每事件独立的键值容器。
type State map[string]any

// Ctx 是一次事件处理过程中的上下文。
//
// 它实现 context.Context，因此可以像 ctx 一样往下传（F-11）。
type Ctx struct {
	ctx    context.Context
	Event  *event.Event
	State  State
	caller transport.Caller

	mu     sync.Mutex
	once   sync.Once
	cached string
}

// NewCtx 构造事件上下文；State 随事件开始为空。
//
//nolint:contextcheck // 仅在调用方传 nil 时兜底 Background；正常路径始终继承调用方 ctx
func NewCtx(ctx context.Context, ev *event.Event, caller transport.Caller) *Ctx {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Ctx{ctx: ctx, Event: ev, State: State{}, caller: caller}
}

// Deadline 转发到内部 ctx。
func (c *Ctx) Deadline() (deadline time.Time, ok bool) { return c.ctx.Deadline() }

// Done 转发到内部 ctx。
func (c *Ctx) Done() <-chan struct{} { return c.ctx.Done() }

// Err 转发到内部 ctx。
func (c *Ctx) Err() error { return c.ctx.Err() }

// Value 转发到内部 ctx。
func (c *Ctx) Value(key any) any { return c.ctx.Value(key) }

// Caller 返回注入的调用器；可能为 nil（例如纯匹配测试）。
func (c *Ctx) Caller() transport.Caller { return c.caller }

// MessageString 返回缓存的纯文本，供多条规则复用。
func (c *Ctx) MessageString() string {
	c.once.Do(func() {
		if c.Event == nil {
			return
		}
		c.cached = c.Event.Message.PlainText()
	})
	return c.cached
}

// Get 按类型无关的方式读取 State。
func (c *Ctx) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.State[key]
	return v, ok
}

// GetString 读取字符串；缺失返回零值与 false。
func (c *Ctx) GetString(key string) (string, bool) {
	v, ok := c.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetInt64 读取整数；接受所有整数宽度。
func (c *Ctx) GetInt64(key string) (int64, bool) {
	v, ok := c.Get(key)
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

// GetBool 读取布尔。
func (c *Ctx) GetBool(key string) (bool, bool) {
	v, ok := c.Get(key)
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// GetStrings 读取字符串切片（F-81 的参数传递形态）。
func (c *Ctx) GetStrings(key string) ([]string, bool) {
	v, ok := c.Get(key)
	if !ok {
		return nil, false
	}
	s, ok := v.([]string)
	if !ok {
		return nil, false
	}
	out := make([]string, len(s))
	copy(out, s)
	return out, true
}

// Set 写入 State（加锁）。
func (c *Ctx) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.State == nil {
		c.State = State{}
	}
	c.State[key] = value
}

// Delete 删除一个键。
func (c *Ctx) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.State, key)
}

// Keys 返回当前键的快照。
func (c *Ctx) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.State))
	for k := range c.State {
		out = append(out, k)
	}
	return out
}

// ResetForNextRoute 在某条路由未匹配、准备尝试下一条时清理 State，
// 但保留以 StateKeyKeepPrefix 开头的键（用于跨路由传参）。
func (c *Ctx) ResetForNextRoute() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.State {
		if strings.HasPrefix(k, StateKeyKeepPrefix) {
			continue
		}
		delete(c.State, k)
	}
}

var _ context.Context = (*Ctx)(nil)
