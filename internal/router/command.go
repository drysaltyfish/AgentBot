package router

import (
	"errors"
	"strings"
	"unicode"
)

// ErrUnterminatedQuote 表示引号没有闭合。
var ErrUnterminatedQuote = errors.New("unterminated quote")

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
