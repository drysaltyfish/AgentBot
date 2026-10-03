// Package tool 实现工具注册表与自描述工具接口（FEATURES.md F-41 / F-42）。
//
// 注册表是"Agent 能用什么能力"的唯一登记处：并发安全、可自省，并能一键导出成
// 模型侧的 function schema。
//
// 顺序稳定性是硬要求而不是洁癖：List/Definitions 的顺序一旦抖动，每次请求的
// 工具段就会不同，直接破坏前缀缓存（F-65）。因此内部用 order 切片维护注册顺序。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalidName 表示工具名不符合模型侧对函数名的要求。
	ErrInvalidName = errors.New("invalid tool name")
	// ErrDuplicate 表示同名工具已注册。
	ErrDuplicate = errors.New("duplicate tool name")
	// ErrNotFound 表示注册表里没有该工具。
	ErrNotFound = errors.New("tool not found")
	// ErrNilTool 表示注册了 nil。
	ErrNilTool = errors.New("register nil tool")
)

// NamePattern 是模型侧对函数名的要求（OpenAI 为 ^[a-zA-Z0-9_-]{1,64}$）。
var NamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// MaxDescription 是工具描述的长度上限；超出会截断并告警。
const MaxDescription = 1024

// Property 描述一个工具参数。
type Property struct {
	Type        string
	Description string
	Enum        []string
	Default     any
	Minimum     *float64
	Maximum     *float64
}

// Schema 描述工具的参数对象。
type Schema struct {
	// Type 为空时按 "object" 处理。
	Type       string
	Properties map[string]Property
	Required   []string
}

// JSONSchema 把参数描述导出成 JSON Schema。
//
// 关键：encoding/json 对 map 的键做排序，因此同一份 Schema 每次序列化结果逐字节相同——
// 这是工具段不破坏前缀缓存的前提。
func (s Schema) JSONSchema() (json.RawMessage, error) {
	props := make(map[string]map[string]any, len(s.Properties))
	for name, p := range s.Properties {
		m := map[string]any{}
		if p.Type != "" {
			m["type"] = p.Type
		}
		if p.Description != "" {
			m["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			m["enum"] = p.Enum
		}
		if p.Default != nil {
			m["default"] = p.Default
		}
		if p.Minimum != nil {
			m["minimum"] = *p.Minimum
		}
		if p.Maximum != nil {
			m["maximum"] = *p.Maximum
		}
		props[name] = m
	}
	root := map[string]any{"properties": props}
	if s.Type != "" {
		root["type"] = s.Type
	} else {
		root["type"] = "object"
	}
	if len(s.Required) > 0 {
		root["required"] = s.Required
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode schema for tool: %w", err)
	}
	return out, nil
}

// Result 是一次工具执行的结果。
//
// Error 非空表示"工具执行失败"——它会被包装成 observation 回灌模型，让模型自行纠错，
// 而不是中断循环（F-35 边界）。
type Result struct {
	Output   string
	Error    string
	Metadata map[string]string
}

// Success 构造成功结果。
func Success(out string) Result { return Result{Output: out} }

// Failure 构造失败结果。
func Failure(msg string) Result { return Result{Error: msg} }

// Failed 判断是否为失败结果。
func (r Result) Failed() bool { return r.Error != "" }

// String 返回回灌给模型看的文本；失败时返回错误文本。
func (r Result) String() string {
	if r.Error != "" {
		return r.Error
	}
	return r.Output
}

// Tool 是自描述工具：模型靠它判断"我是谁、参数是什么"。
type Tool interface {
	Name() string
	Description() string
	Parameters() Schema
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// 属性通过可选接口声明，而不是塞进 Tool 接口：绝大多数工具只关心默认值，
// 强迫每个实现都写三个 false 方法只会增加噪音。
//
// 三个属性分别服务于：
//   - ReadOnly：F-45 审批（只读工具通常无需人工确认）
//   - ConcurrencySafe：F-35 的 ParallelTools
//   - Dangerous：F-45 审批（危险工具默认需要确认）
type ReadOnlyTool interface{ ReadOnly() bool }

// ConcurrencySafeTool 声明该工具可并发执行。
type ConcurrencySafeTool interface{ ConcurrencySafe() bool }

// DangerousTool 声明该工具具有副作用。
type DangerousTool interface{ Dangerous() bool }

// IsReadOnly 返回工具是否声明为只读；未声明时按 false（保守）。
func IsReadOnly(t Tool) bool {
	if v, ok := t.(ReadOnlyTool); ok {
		return v.ReadOnly()
	}
	return false
}

// IsConcurrencySafe 返回工具是否声明可并发；未声明时按 false（保守，串行执行）。
func IsConcurrencySafe(t Tool) bool {
	if v, ok := t.(ConcurrencySafeTool); ok {
		return v.ConcurrencySafe()
	}
	return false
}

// IsDangerous 返回工具是否声明有副作用；未声明时按 true（保守，走审批）。
func IsDangerous(t Tool) bool {
	if v, ok := t.(DangerousTool); ok {
		return v.Dangerous()
	}
	return true
}

// clampDescription 按上限截断描述并告警。
func clampDescription(name, desc string, warn func(string)) string {
	r := []rune(desc)
	if len(r) <= MaxDescription {
		return desc
	}
	if warn != nil {
		warn(fmt.Sprintf("tool %q description truncated from %d to %d runes", name, len(r), MaxDescription))
	}
	return string(r[:MaxDescription])
}
