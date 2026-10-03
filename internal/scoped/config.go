package scoped

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Config 是按作用域分层的配置。
//
// 每层记录"显式设置过的键"：键存在即显式设置，哪怕值是零值或 nil；
// 因此零值不能表达缺省。所有方法对 nil 接收者安全，但空接收者无法写入，
// 正常路径请用 NewConfig 构造。
type Config struct {
	layers map[string]map[string]any
}

// NewConfig 构造空配置。
func NewConfig() *Config { return &Config{layers: map[string]map[string]any{}} }

// Set 在 scope 层显式设置 key。scope 为空表示全局层；key 为空则忽略。
func (c *Config) Set(scope, key string, value any) {
	if c == nil || key == "" {
		return
	}
	if c.layers == nil {
		c.layers = map[string]map[string]any{}
	}
	scope = CleanScope(scope)
	layer := c.layers[scope]
	if layer == nil {
		layer = map[string]any{}
		c.layers[scope] = layer
	}
	layer[key] = value
}

// Merge 把 values 批量写入 scope 层；nil map 无操作。
func (c *Config) Merge(scope string, values map[string]any) {
	if c == nil {
		return
	}
	for k, v := range values {
		c.Set(scope, k, v)
	}
}

// Get 按 scope → 父级 → 全局逐级回退取 key。
//
// 返回值 ok 表示是否显式设置过：显式零值会带 ok=true 返回，不会被当成缺省。
// scope 为空时只查全局层。
func (c *Config) Get(scope, key string) (any, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	for s := CleanScope(scope); ; s = Parent(s) {
		if v, ok := c.layers[s][key]; ok {
			return v, true
		}
		if s == "" {
			return nil, false
		}
	}
}

// GetScoped 按 scopes 给出的优先级（高 → 低）逐层精确查找 key，最后落到全局层。
//
// 与 Get 的递归回退不同，这里不做父路径回退：每个 scope 只查自己那一层。
// 需要"用户 → 群 → 人格 → 全局"这类多分支优先级时用本方法（见 SessionScopes）。
func (c *Config) GetScoped(key string, scopes ...string) (any, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	for _, s := range scopes {
		if layer := c.layers[CleanScope(s)]; layer != nil {
			if v, ok := layer[key]; ok {
				return v, true
			}
		}
	}
	v, ok := c.layers[""][key]
	return v, ok
}

// Resolve 取 key 并断言为 T；未设置或类型不符都返回明确错误。
//
// 未知 scope 不会静默返回零值：只要回退链上都没有该键就报错。
func Resolve[T any](c *Config, scope, key string) (T, error) {
	var zero T
	v, ok := c.Get(scope, key)
	if !ok {
		return zero, fmt.Errorf("scoped: 配置项 %q 在作用域 %q 及其回退链上均未设置", key, CleanScope(scope))
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("scoped: 配置项 %q 的类型为 %T，无法解析为 %T", key, v, zero)
	}
	return typed, nil
}

// Scopes 返回已设置过的作用域层，按路径排序（全局层为空串，排在最前）。
func (c *Config) Scopes() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.layers))
	for s := range c.layers {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Keys 返回某层显式设置过的键，按字典序排列。
func (c *Config) Keys(scope string) []string {
	if c == nil {
		return nil
	}
	layer := c.layers[CleanScope(scope)]
	out := make([]string, 0, len(layer))
	for k := range layer {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LoadFile 把 YAML 文件里的键值并入 scope 层。
//
// 解析失败返回错误且不修改任何层：调用方据此告警并保留旧值，服务不中断（F-24）。
func (c *Config) LoadFile(scope, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("scoped: 读取作用域文件 %s: %w", path, err)
	}
	values := map[string]any{}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("scoped: 解析作用域文件 %s: %w", path, err)
	}
	c.Merge(scope, values)
	return nil
}
