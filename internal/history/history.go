// Package history 实现对话历史管理（FEATURES.md F-38）。
//
// 独立成包的原因是会话（F-21）需要持有 History，而 Agent（F-34）也要用它；
// 放在任何一方都会造成反向依赖。
package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEmpty 表示历史为空。
var ErrEmpty = errors.New("history is empty")

// Kind 是历史条目类型。
type Kind string

// 条目类型常量。
const (
	KindUser       Kind = "user"
	KindAssistant  Kind = "assistant"
	KindToolCall   Kind = "tool_call"
	KindToolResult Kind = "tool_result"
	KindMarker     Kind = "marker"
)

// ToolCall 是 assistant 发起的一次工具调用。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Item 是一条历史条目。
type Item struct {
	Kind    Kind
	Content string
	// Name 是工具名（工具轮次用）。
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
	At         time.Time
	// SpeakerID / SpeakerName 标识发言人（群聊才有；私聊为 0）。
	//
	// 发言人**不进 Content**：Content 保持干净，检索返回的正文才干净、
	// 全文索引也不会被 "[QQ123]" 这种前缀污染。标签在渲染时生成。
	SpeakerID   int64
	SpeakerName string
}

// Clone 返回深拷贝，避免调用方改动内部状态（F-38 边界）。
func (i Item) Clone() Item {
	cp := i
	if i.ToolCalls != nil {
		cp.ToolCalls = append([]ToolCall(nil), i.ToolCalls...)
	}
	return cp
}

// History 是历史存储接口。
type History interface {
	Append(ctx context.Context, key string, item Item) error
	Messages(ctx context.Context, key string) ([]Item, error)
	Reset(ctx context.Context, key string) error
	Trim(ctx context.Context, key string, n int) error
}

// Trimmer 是裁剪策略。
//
// M1 只实现 Window；TokenBudget 与 Summarize 留到 M3（接 F-32 的预算）。
type Trimmer interface {
	Apply(items []Item) []Item
}

// Window 保留最近 n 条，但绝不拆散 assistant 的 tool_calls 与其后的 tool 结果。
type Window struct {
	N int
}

// HighWater 是高水位批量裁剪：只有超过 Max 才回收到 Low。
//
// 为什么不能直接用 Window：Memory 在每次 Append 超限时都会调用裁剪，Window 等价于
// "每轮都把历史缩短一条"，于是每次请求的消息前缀都不同——DeepSeek 的前缀缓存因此
// 永远无法命中（实测命中率恒为 0）。HighWater 把裁剪摊薄到每 (Max-Low) 条一次，
// 让前缀在两次裁剪之间保持稳定，缓存才真正可用。
type HighWater struct {
	Max int
	Low int
}

// Apply 实现 Trimmer：未超 Max 时原样返回，超了才回收到 Low。
func (h HighWater) Apply(items []Item) []Item {
	if h.Max <= 0 || len(items) <= h.Max {
		return items
	}
	low := h.Low
	if low <= 0 || low >= h.Max {
		low = h.Max * 3 / 4
	}
	// 复用 Window 的对齐逻辑，保证不拆散 tool 调用与结果。
	return Window{N: low}.Apply(items)
}

// Apply 实现 Trimmer。
func (w Window) Apply(items []Item) []Item {
	if w.N <= 0 || len(items) <= w.N {
		return items
	}
	start := len(items) - w.N
	end := len(items)

	// 1) 尾部若是"带 tool_calls 的 assistant"，其结果已被裁掉 → 一并丢弃。
	for end > start {
		last := items[end-1]
		if last.Kind == KindAssistant && len(last.ToolCalls) > 0 {
			end--
			continue
		}
		break
	}
	// 2) 头部若是孤立的 tool 结果：优先向前找回发起它的 assistant（宁可超出 N，
	//    也不拆散调用与结果）；实在找不到才丢弃。
	for start < end && items[start].Kind == KindToolResult && !hasOwner(items, start, end, start) {
		owner := -1
		want := items[start].ToolCallID
		for j := start - 1; j >= 0; j-- {
			if items[j].Kind != KindAssistant {
				continue
			}
			for _, tc := range items[j].ToolCalls {
				if tc.ID == want {
					owner = j
					break
				}
			}
			if owner >= 0 {
				break
			}
		}
		if owner < 0 {
			start++
			continue
		}
		start = owner
		break
	}
	// 3) 反向：窗口内 assistant 的 tool_calls 若结果被裁掉一半，则整体丢弃该 assistant。
	for start < end {
		last := items[end-1]
		if last.Kind == KindAssistant && len(last.ToolCalls) > 0 {
			end--
			continue
		}
		break
	}
	if start >= end {
		return []Item{}
	}
	out := make([]Item, 0, end-start)
	for _, it := range items[start:end] {
		out = append(out, it.Clone())
	}
	return out
}

// hasOwner 判断 resultIdx 处的 tool 结果在 [start, resultIdx) 内是否有发起它的 assistant。
func hasOwner(items []Item, start, end, resultIdx int) bool {
	if resultIdx < start || resultIdx >= end {
		return false
	}
	id := items[resultIdx].ToolCallID
	for j := start; j < resultIdx; j++ {
		if items[j].Kind != KindAssistant {
			continue
		}
		for _, tc := range items[j].ToolCalls {
			if tc.ID == id {
				return true
			}
		}
	}
	return false
}

// Orphans 返回孤立的 tool 结果数量（测试与自检用）。
func Orphans(items []Item) int {
	n := 0
	for i := range items {
		if items[i].Kind == KindToolResult && !hasOwner(items, 0, len(items), i) {
			n++
		}
	}
	return n
}

// DanglingToolCalls 返回"带 tool_calls 但没有对应结果"的 assistant 数量。
func DanglingToolCalls(items []Item) int {
	results := map[string]struct{}{}
	for _, it := range items {
		if it.Kind == KindToolResult {
			results[it.ToolCallID] = struct{}{}
		}
	}
	n := 0
	for _, it := range items {
		if it.Kind != KindAssistant || len(it.ToolCalls) == 0 {
			continue
		}
		for _, tc := range it.ToolCalls {
			if _, ok := results[tc.ID]; !ok {
				n++
				break
			}
		}
	}
	return n
}

// RenderText 返回给模型看的正文：发言人标签 + 时间 + 原内容。
//
// 为什么标签在**渲染时**生成，而不是写库时烤进 Content：
//   - 检索（recall_history）返回的正文保持干净，用户看到的是原话
//   - 全文索引不被 "[QQ123] (10-03 18:23)" 这类前缀污染
//     （否则搜 "QQ" 会命中全部消息）
//   - 格式演进不必重写历史——这是"只追加"纪律能继续成立的前提
//
// 标签同时给出**昵称与 QQ 号**：QQ 号是稳定锚点（昵称改了也认得出是同一个人），
// 昵称让模型能自然称呼对方而不必每次去查。这也是调研推荐的形式
// （who said what, and when）。
func (i Item) RenderText() string {
	if i.SpeakerID <= 0 {
		return i.Content
	}
	label := fmt.Sprintf("[QQ%d]", i.SpeakerID)
	if name := strings.TrimSpace(i.SpeakerName); name != "" {
		label = fmt.Sprintf("%s[QQ%d]", name, i.SpeakerID)
	}
	if i.At.IsZero() {
		return label + " " + i.Content
	}
	return fmt.Sprintf("%s (%s) %s", label, i.At.Format("01-02 15:04"), i.Content)
}
