package router

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrUnterminatedQuote 表示引号没有闭合。
	ErrUnterminatedQuote = errors.New("unterminated quote")
	// ErrUnsupportedFlagType 表示字段类型不支持绑定。
	ErrUnsupportedFlagType = errors.New("unsupported flag field type")
	// ErrBadReceiver 表示 BindFlags 的入参不是"指向结构体的非 nil 指针"。
	ErrBadReceiver = errors.New("BindFlags wants a non-nil pointer to struct")
)

// ParseCommandArgs 用 shellwords 规则切分命令参数。
//
// 支持单引号（原样）、双引号、反斜杠转义与连续空白折叠；引号未闭合返回错误。
func ParseCommandArgs(s string) ([]string, error) {
	args := make([]string, 0, 4)
	var (
		cur      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
		started  bool
	)
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			started = true
		case r == '\\' && !inSingle:
			escaped = true
			started = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case unicode.IsSpace(r) && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inSingle || inDouble {
		return nil, ErrUnterminatedQuote
	}
	if escaped {
		cur.WriteRune('\\')
	}
	flush()
	return args, nil
}

// BindOptions 控制 flag 绑定行为。
type BindOptions struct {
	// IgnoreUnknown 为真时忽略未定义的 flag，而不是报错。
	IgnoreUnknown bool
}

// BindFlags 按 flag:"name,default=x" tag 把命令行参数绑定到结构体字段（严格模式）。
func BindFlags(v any, args []string) error {
	return BindFlagsOpt(v, args, BindOptions{})
}

// BindFlagsOpt 是 BindFlags 的可配置版本。
//
// 支持类型：bool / int / int64 / float64 / string / time.Duration。
func BindFlagsOpt(v any, args []string, opts BindOptions) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("%w: got %T", ErrBadReceiver, v)
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return fmt.Errorf("%w: got pointer to %s", ErrBadReceiver, elem.Kind())
	}

	fs := flag.NewFlagSet("command", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	known := map[string]struct{}{}
	durationType := reflect.TypeOf(time.Duration(0))

	for i := 0; i < elem.NumField(); i++ {
		sf := elem.Type().Field(i)
		tag := sf.Tag.Get("flag")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		def := ""
		for _, p := range parts[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "default=") {
				def = strings.TrimPrefix(p, "default=")
			}
		}
		field := elem.Field(i)
		if !field.CanSet() {
			return fmt.Errorf("%w: field %s is not settable", ErrUnsupportedFlagType, sf.Name)
		}
		switch {
		case field.Kind() == reflect.Bool:
			d, _ := strconv.ParseBool(def)
			fs.BoolVar(field.Addr().Interface().(*bool), name, d, "")
		case field.Type() == durationType:
			var d time.Duration
			if def != "" {
				parsed, err := time.ParseDuration(def)
				if err != nil {
					return fmt.Errorf("field %s: invalid default duration %q: %w", sf.Name, def, err)
				}
				d = parsed
			}
			fs.DurationVar(field.Addr().Interface().(*time.Duration), name, d, "")
		case field.Kind() == reflect.Int:
			d, _ := strconv.Atoi(def)
			fs.IntVar(field.Addr().Interface().(*int), name, d, "")
		case field.Kind() == reflect.Int64:
			d, _ := strconv.ParseInt(def, 10, 64)
			fs.Int64Var(field.Addr().Interface().(*int64), name, d, "")
		case field.Kind() == reflect.Float64:
			d, _ := strconv.ParseFloat(def, 64)
			fs.Float64Var(field.Addr().Interface().(*float64), name, d, "")
		case field.Kind() == reflect.String:
			fs.StringVar(field.Addr().Interface().(*string), name, def, "")
		default:
			return fmt.Errorf("%w: field %s has type %s", ErrUnsupportedFlagType, sf.Name, field.Type())
		}
		known[name] = struct{}{}
	}

	toParse := args
	if opts.IgnoreUnknown {
		toParse = filterKnownFlags(args, known)
	}
	if err := fs.Parse(toParse); err != nil {
		return err
	}
	return nil
}

func filterKnownFlags(args []string, known map[string]struct{}) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			if _, ok := known[name]; !ok {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}
