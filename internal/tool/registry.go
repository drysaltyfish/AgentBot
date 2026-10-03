package tool

import (
	"fmt"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// Registry 是工具注册表。
//
// 零值不可用，请用 New 构造。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
	warn  func(string)
}

// Option 配置 Registry。
type Option func(*Registry)

// WithWarnFunc 注入告警回调（例如描述被截断时）。
func WithWarnFunc(fn func(string)) Option {
	return func(r *Registry) { r.warn = fn }
}

// New 构造空注册表。
func New(opts ...Option) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Register 注册一个工具。名称非法或重复时返回错误。
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return ErrNilTool
	}
	name := t.Name()
	if !NamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q（要求 ^[a-zA-Z0-9_-]{1,64}$）", ErrInvalidName, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
	return nil
}

// MustRegister 与 Register 相同，但出错时 panic。
//
// 仅供 init 阶段使用（与 regexp.MustCompile 同一惯例）。F-73 明确把"init 期断言"
// 列为 panic 禁令的例外，因此这里的定向豁免是符合规约的，而不是绕过它。
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		//nolint:forbidigo // F-73 允许 init 期断言 panic；Must* 是 Go 的既定惯例
		panic(err)
	}
}

// Get 按名字取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 按注册顺序返回工具。
//
// 顺序必须稳定：它决定的工具段每次都要逐字节相同，否则破坏前缀缓存。
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// Names 按注册顺序返回工具名。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if _, ok := r.tools[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// Len 返回工具数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}

// Definitions 导出给模型的 function schema，顺序与 List 一致。
func (r *Registry) Definitions() []llm.ToolSpec {
	tools := r.List()
	out := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		params, err := t.Parameters().JSONSchema()
		if err != nil {
			// 导出失败不该让整份 schema 变形：退化成空对象并告警。
			if r.warn != nil {
				r.warn(fmt.Sprintf("tool %q schema is not encodable, falling back to an empty object: %v", t.Name(), err))
			}
			params = []byte("{\"type\":\"object\",\"properties\":{}}")
		}
		out = append(out, llm.ToolSpec{
			Name:        t.Name(),
			Description: clampDescription(t.Name(), t.Description(), r.warn),
			Parameters:  params,
		})
	}
	return out
}

// Subset 派生一个只含指定工具的子注册表，供不同 Worker 使用不同工具集（F-37）。
//
// 不存在的名字会被忽略；子注册表与父注册表完全独立（后续注册互不影响）。
func (r *Registry) Subset(names ...string) *Registry {
	sub := New()
	if r.warn != nil {
		sub.warn = r.warn
	}
	for _, name := range names {
		if t, ok := r.Get(name); ok {
			sub.MustRegister(t)
		}
	}
	return sub
}

// Remove 移除一个工具；不存在时返回 false。
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// Clear 清空注册表（主要供测试）。
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = map[string]Tool{}
	r.order = nil
}
