// Package memory 实现长期记忆的存储与写入决策（FEATURES.md F-87 / F-88）。
//
// 独立成包的理由：记忆的写入决策需要同时用到**存储层**（找相似条目、落库）与
// **模型层**（歧义带里做语义判断）。把它放在任何一侧都会造成反向依赖，
// 而放在 Agent 里会让 Agent 硬依赖具体存储。
package memory

import (
	"errors"
	"strings"
)

var (
	// ErrEmpty 表示记忆内容为空。
	ErrEmpty = errors.New("memory text is empty")
	// ErrMultiline 表示记忆含换行（F-40 要求单行）。
	ErrMultiline = errors.New("memory text must be a single line")
	// ErrTooLong 表示记忆超长。
	ErrTooLong = errors.New("memory text is too long")
	// ErrUnavailable 表示记忆存储未配置。
	ErrUnavailable = errors.New("memory store is not configured")
	// ErrJudgeUnparsed 表示判官的回答无法解析成"是/否"。
	ErrJudgeUnparsed = errors.New("cannot parse judge verdict")
)

// Limit 是单条记忆的长度上限（字符）。
//
// 取值对齐 F-47："单条记忆长度上限（默认 500 字符）"。
const Limit = 500

// Validate 做写入前的统一校验。
//
// 刻意返回错误而不是截断：截断会让模型以为整条存下来了，
// 属于"看起来成功、实际丢数据"。
func Validate(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	switch {
	case trimmed == "":
		return "", ErrEmpty
	case strings.ContainsAny(trimmed, "\r\n"):
		return "", ErrMultiline
	case len([]rune(trimmed)) > Limit:
		return "", ErrTooLong
	}
	return trimmed, nil
}
