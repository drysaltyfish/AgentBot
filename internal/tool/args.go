package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ArgError 是一条结构化的参数错误。
//
// 结构化而不是一句人话，是为了让模型能精确知道"哪个字段、违反了什么规则、实际收到什么"，
// 从而自我纠正——这正是 F-35"失败也要回灌"能起作用的前提。
type ArgError struct {
	Field string
	Rule  string
	Got   string
}

// Error 实现 error。
func (e *ArgError) Error() string {
	return fmt.Sprintf("参数 %s 不满足 %s（收到 %s）", e.Field, e.Rule, e.Got)
}

// ArgErrors 汇总多个参数问题。
//
// 刻意**一次报全部**问题而不是遇到第一个就返回：模型一次就能改对，省一轮往返。
type ArgErrors []*ArgError

// Error 实现 error。
func (e ArgErrors) Error() string {
	parts := make([]string, 0, len(e))
	for _, p := range e {
		parts = append(parts, p.Error())
	}
	return "参数校验失败：" + strings.Join(parts, "；")
}

// ParseArgs 解码并校验工具参数。
//
// tag 语法（与常见 Go 库一致）：
//
//	arg:"name"               重命名（默认用字段名的小写）
//	arg:"name,required"      必填
//	arg:"name,enum=a|b|c"    枚举
//	arg:"name,default=10"    默认值
//	arg:"-"                  忽略该字段
//
// 未知字段**默认忽略**（模型常多传），需要严格时报错请用 ParseArgsStrict。
// 类型不匹配时会先尝试宽松转换（模型把 10 写成 "10" 很常见），仍失败才报错。
func ParseArgs[T any](raw json.RawMessage) (T, error) { return parseArgs[T](raw, false) }

// ParseArgsStrict 与 ParseArgs 相同，但多传的未知字段会报错。
//
// 说明：规格里写的是"通过 tag 开启严格模式"。Go 没有结构体级 tag，字段级标记又会
// 污染参数结构体，因此这里改为独立函数——语义更清楚，也不给参数结构体加噪音。
func ParseArgsStrict[T any](raw json.RawMessage) (T, error) { return parseArgs[T](raw, true) }

func parseArgs[T any](raw json.RawMessage, strict bool) (T, error) {
	var out T
	rv := reflect.ValueOf(&out).Elem()
	rt := rv.Type()
	if rt.Kind() != reflect.Struct {
		return out, fmt.Errorf("ParseArgs 只支持结构体，实际为 %s", rt.Kind())
	}

	fields := map[string]json.RawMessage{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return out, fmt.Errorf("参数必须是 JSON 对象: %w", err)
		}
	}

	var problems ArgErrors
	known := make(map[string]bool, rt.NumField())

	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, required, enum, def, skip := parseArgTag(sf)
		if skip {
			continue
		}
		known[name] = true

		rawVal, present := fields[name]
		if !present || isJSONNull(rawVal) {
			if def != "" {
				if err := setField(rv.Field(i), json.RawMessage(def)); err != nil {
					problems = append(problems, &ArgError{Field: name, Rule: "default=" + def, Got: def})
				}
				continue
			}
			if required {
				problems = append(problems, &ArgError{Field: name, Rule: "required", Got: "缺失"})
			}
			continue
		}

		if err := setField(rv.Field(i), rawVal); err != nil {
			problems = append(problems, &ArgError{Field: name, Rule: describeKind(sf.Type), Got: string(rawVal)})
			continue
		}
		if len(enum) > 0 {
			got := fmt.Sprint(rv.Field(i).Interface())
			if !containsString(enum, got) {
				problems = append(problems, &ArgError{Field: name, Rule: "enum=" + strings.Join(enum, "|"), Got: got})
			}
		}
	}

	if strict {
		for k := range fields {
			if !known[k] {
				problems = append(problems, &ArgError{Field: k, Rule: "未知字段", Got: "多传"})
			}
		}
	}
	if len(problems) > 0 {
		return out, problems
	}
	return out, nil
}

// parseArgTag 解析 arg tag。
func parseArgTag(sf reflect.StructField) (name string, required bool, enum []string, def string, skip bool) {
	tag := sf.Tag.Get("arg")
	if tag == "-" {
		return "", false, nil, "", true
	}
	name = strings.ToLower(sf.Name)
	if tag == "" {
		return name, false, nil, "", false
	}
	parts := strings.Split(tag, ",")
	if strings.TrimSpace(parts[0]) != "" {
		name = strings.TrimSpace(parts[0])
	}
	for _, opt := range parts[1:] {
		switch {
		case opt == "required":
			required = true
		case strings.HasPrefix(opt, "enum="):
			for _, v := range strings.Split(strings.TrimPrefix(opt, "enum="), "|") {
				if v = strings.TrimSpace(v); v != "" {
					enum = append(enum, v)
				}
			}
		case strings.HasPrefix(opt, "default="):
			def = strings.TrimPrefix(opt, "default=")
		}
	}
	return name, required, enum, def, false
}

// setField 赋值；失败时先尝试宽松转换。
func setField(dst reflect.Value, raw json.RawMessage) error {
	if err := json.Unmarshal(raw, dst.Addr().Interface()); err == nil {
		return nil
	}
	// 宽松转换：模型把数字/布尔写成字符串很常见。
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return errors.New("type mismatch")
	}
	// reflect.Kind 有二十多个取值，这里只处理可宽松转换的标量；其余落到 default 报错。
	//nolint:exhaustive // 上面的 json.Unmarshal 已覆盖其它类型，这里只做标量兜底
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(s)
	case reflect.Bool:
		v, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		dst.SetBool(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		dst.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		dst.SetUint(v)
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		dst.SetFloat(v)
	default:
		return errors.New("type mismatch")
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func describeKind(t reflect.Type) string {
	// 只有少数几种需要人类可读的名字，其余直接用 Kind 的字符串。
	//nolint:exhaustive // 其余取值走 default，走 Kind().String() 足够
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "unsigned integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return t.Kind().String()
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
