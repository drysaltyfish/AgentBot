package router

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// bindTag 是状态绑定的 struct tag 名。
const bindTag = "bot"

// fieldBinding 描述一个字段与 State 键的映射。
type fieldBinding struct {
	Index int
	Key   string
}

// bindInfo 是一种结构体类型的绑定方案（构造后只读）。
type bindInfo struct {
	fields      []fieldBinding
	unsupported []string
}

// bindCache 缓存 reflect.Type -> bindInfo。
//
// 规格（F-22）明确要求"按 reflect.Type 缓存进 sync.Map"，因此这里是包级变量。
// 它只做记忆化：键是类型、值是构造后只读的切片，不承载任何语义状态
// （与"全局可变路由表"那类反模式不同）。
var bindCache sync.Map

// Bind 按 `bot:"stateKey"` 标签把 State 里的值回填到 v 指向的结构体（F-22）。
//
// 语义：
//   - key 不存在 -> 保持零值，不报错（可选参数自然为零值）；
//   - 类型不匹配 -> 返回带字段名与期望类型的错误，绝不 panic；
//   - nil / 非指针 / 指向非结构体 -> 返回错误；
//   - 不支持的字段类型 -> 跳过该字段，并在返回值里列出。
func (c *Ctx) Bind(v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("router: Bind 需要一个非 nil 指针，实际为 %T", v)
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return fmt.Errorf("router: Bind 需要指向结构体，实际指向 %s", elem.Kind())
	}

	info := bindInfoFor(elem.Type())
	var problems []string
	for _, b := range info.fields {
		raw, ok := c.State[b.Key]
		if !ok {
			continue
		}
		if err := setBoundField(elem.Field(b.Index), raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", elem.Type().Field(b.Index).Name, err))
		}
	}
	if len(info.unsupported) > 0 {
		problems = append(problems, "不支持的字段类型: "+strings.Join(info.unsupported, ", "))
	}
	if len(problems) > 0 {
		return fmt.Errorf("router: 状态绑定失败: %s", strings.Join(problems, "; "))
	}
	return nil
}

// bindInfoFor 取（或构建）类型的绑定方案。
func bindInfoFor(t reflect.Type) bindInfo {
	if cached, ok := bindCache.Load(t); ok {
		return cached.(bindInfo)
	}
	var info bindInfo
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get(bindTag)
		if key == "" || !f.IsExported() {
			continue
		}
		if !supportedBindType(f.Type) {
			info.unsupported = append(info.unsupported, f.Name)
			continue
		}
		info.fields = append(info.fields, fieldBinding{Index: i, Key: key})
	}
	bindCache.Store(t, info)
	return info
}

// supportedBindType 判断字段类型是否可绑定（F-22 支持的类型集合）。
func supportedBindType(t reflect.Type) bool {
	kind := t.Kind()
	if kind == reflect.Slice {
		return t.Elem().Kind() == reflect.String
	}
	// 用条件表达式而不是对 reflect.Kind 的 switch：那是个大枚举，
	// 穷举检查只会制造噪音。
	return kind == reflect.String || kind == reflect.Bool ||
		kind == reflect.Int || kind == reflect.Int64 ||
		kind == reflect.Float64 || kind == reflect.Interface
}

func setBoundField(field reflect.Value, raw any) error {
	if raw == nil {
		return fmt.Errorf("期望 %s，实际为 nil", field.Type())
	}
	kind := field.Kind()
	switch {
	case kind == reflect.String:
		s, ok := raw.(string)
		if !ok {
			return bindTypeErr(field.Type(), raw)
		}
		field.SetString(s)
	case kind == reflect.Int || kind == reflect.Int64:
		switch n := raw.(type) {
		case int:
			field.SetInt(int64(n))
		case int64:
			field.SetInt(n)
		default:
			return bindTypeErr(field.Type(), raw)
		}
	case kind == reflect.Bool:
		b, ok := raw.(bool)
		if !ok {
			return bindTypeErr(field.Type(), raw)
		}
		field.SetBool(b)
	case kind == reflect.Float64:
		f, ok := raw.(float64)
		if !ok {
			return bindTypeErr(field.Type(), raw)
		}
		field.SetFloat(f)
	case kind == reflect.Slice:
		ss, ok := raw.([]string)
		if !ok {
			return bindTypeErr(field.Type(), raw)
		}
		field.Set(reflect.ValueOf(ss))
	case kind == reflect.Interface:
		field.Set(reflect.ValueOf(raw))
	default:
		return bindTypeErr(field.Type(), raw)
	}
	return nil
}

func bindTypeErr(want reflect.Type, got any) error {
	return fmt.Errorf("期望 %s，实际为 %T", want, got)
}

// CommandModel 对应 Command 规则（F-14）写入的状态。
type CommandModel struct {
	Command string   `bot:"command"`
	Args    []string `bot:"args"`
}

// RegexModel 对应 Regexp 规则写入的子匹配。
type RegexModel struct {
	Match []string `bot:"regex_match"`
}

// ImageModel 对应 HasImage 规则写入的图片地址。
type ImageModel struct {
	URLs []string `bot:"image_urls"`
}
