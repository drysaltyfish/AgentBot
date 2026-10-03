package tool

import "fmt"

// Sandbox 用同一份策略包装注册表里的每个工具，返回全新的注册表。
//
// 这是"启动期校验"的入口：策略非法、任一工具无法装配时都返回错误，不会留下
// 半装配的注册表。原注册表保持不变，供无沙箱场景使用。
func (r *Registry) Sandbox(p *Policy) (*Registry, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out := New()
	if r.warn != nil {
		out.warn = r.warn
	}
	for _, t := range r.List() {
		wrapped, err := NewSandboxTool(t, p)
		if err != nil {
			return nil, fmt.Errorf("装配工具 %q 的沙箱: %w", t.Name(), err)
		}
		if err := out.Register(wrapped); err != nil {
			return nil, err
		}
	}
	return out, nil
}
