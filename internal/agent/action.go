package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoActions 表示输入里没有任何可解析的动作。
	ErrNoActions = errors.New("no actions parsed")
	// ErrNoLLM 表示 Agent 没有配置 LLM。
	ErrNoLLM = errors.New("agent has no llm configured")
)

// DefaultMaxActions 是单次解析允许的最大动作数（防模型刷屏）。
const DefaultMaxActions = 16

// fence 是 Markdown 代码块围栏。
const fence = "```"

// Action 是模型输出的一个动作。
type Action struct {
	Name   string
	Params map[string]any
}

// ParseResult 是解析结果。
type ParseResult struct {
	Actions  []Action
	Warnings []string
}

// ParseActions 解析模型输出的动作流。
//
// 容错策略（F-39）：剥代码块围栏 → 按 {} 边界切分 → UseNumber 解码 → 跳过空 action
// → 非法片段记录警告并从下一个 { 继续。纯文本输入返回 ErrNoActions。
func ParseActions(text string, maxActions int) (*ParseResult, error) {
	if maxActions <= 0 {
		maxActions = DefaultMaxActions
	}
	res := &ParseResult{}
	body := stripCodeFence(strings.TrimSpace(text))
	if body == "" {
		return res, ErrNoActions
	}

	for _, candidate := range splitObjects(body) {
		var obj map[string]any
		dec := json.NewDecoder(bytes.NewReader([]byte(candidate)))
		dec.UseNumber() // 不加会把 int64 静默降级为 float64
		if err := dec.Decode(&obj); err != nil {
			res.Warnings = append(res.Warnings, "skipped invalid fragment: "+truncateForWarning(candidate))
			continue
		}
		name, _ := obj["action"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			res.Warnings = append(res.Warnings, "skipped entry with empty action")
			continue
		}
		params := map[string]any{}
		if raw, ok := obj["params"]; ok && raw != nil {
			m, ok := raw.(map[string]any)
			if !ok {
				res.Warnings = append(res.Warnings, "skipped "+name+": params is not an object")
				continue
			}
			params = m
		}
		if len(res.Actions) >= maxActions {
			res.Warnings = append(res.Warnings, fmt.Sprintf("action limit %d reached; remaining entries truncated", maxActions))
			break
		}
		res.Actions = append(res.Actions, Action{Name: name, Params: params})
	}

	if len(res.Actions) == 0 {
		if len(res.Warnings) == 0 {
			res.Warnings = append(res.Warnings, "input contains no JSON object")
		}
		return res, ErrNoActions
	}
	return res, nil
}

// stripCodeFence 去掉一层 Markdown 代码块围栏。
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, fence) && !strings.HasPrefix(s, "~~~") {
		return s
	}
	firstNL := strings.IndexByte(s, '\n')
	if firstNL < 0 {
		return s
	}
	body := s[firstNL+1:]
	if idx := strings.LastIndex(body, fence); idx >= 0 {
		body = body[:idx]
	} else if idx := strings.LastIndex(body, "~~~"); idx >= 0 {
		body = body[:idx]
	}
	return strings.TrimSpace(body)
}

// splitObjects 按大括号配对切分顶层 JSON 对象（不依赖 dec.More() 的边界语义）。
func splitObjects(s string) []string {
	var out []string
	depth := 0
	start := -1
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, s[start:i+1])
					start = -1
				}
			}
		}
	}
	return out
}

func truncateForWarning(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "..."
}

// ParamString 从动作参数里取字符串。
func (a Action) ParamString(key string) (string, bool) {
	v, ok := a.Params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// ParamInt64 从动作参数里取整数（保留 UseNumber 带来的精度）。
func (a Action) ParamInt64(key string) (int64, bool) {
	v, ok := a.Params[key]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

// ParamRaw 返回参数的原始 JSON 表示。
func (a Action) ParamRaw(key string) string {
	v, ok := a.Params[key]
	if !ok {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
