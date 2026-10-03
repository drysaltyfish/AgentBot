package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// HistoryReader 是 recall_history 需要的能力；history.History 天然满足。
type HistoryReader interface {
	Messages(ctx context.Context, key string) ([]history.Item, error)
}

// HistorySearcher 是历史存储的**可选**检索能力（F-84）。
//
// 能存不等于能搜：只有支持检索的实现才提供它。有检索时走索引并拿到片段与上下文，
// 没有时才退化为"读全量再子串过滤"——后者在大历史上是 O(n)，且给不出片段。
type HistorySearcher interface {
	Search(ctx context.Context, key, query string, limit int) ([]history.Hit, error)
}

// DefaultRecallLimit 是一次召回最多返回多少条。
const DefaultRecallLimit = 10

// MaxRecallLimit 是召回条数的硬上限，防止模型一次拉空整个历史。
const MaxRecallLimit = 50

type recallHistory struct{ deps Deps }

func (recallHistory) Name() string { return "recall_history" }
func (recallHistory) Description() string {
	return "检索本次会话之前聊过的内容；可给关键词，也可留空取最近几条"
}
func (recallHistory) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"query": {Type: "string", Description: "关键词，留空则返回最近几条"},
			"limit": {Type: "integer", Description: "最多返回几条，默认 10，上限 50"},
		},
	}
}
func (recallHistory) ReadOnly() bool        { return true }
func (recallHistory) ConcurrencySafe() bool { return true }

type recallHistoryArgs struct {
	Query string `arg:"query"`
	Limit int    `arg:"limit"`
}

// Execute 按当前会话读取历史。
//
// 会话键从 ctx 取（tool.ScopeFrom）：工具签名固定为 Execute(ctx, args)，
// 而"这次调用属于哪个会话"本来就是上下文信息。取不到就明确失败——
// 绝不能退化成"读全部会话的历史"，那会串台。
// renderHits 把检索结果渲染成给模型看的紧凑文本。
//
// 带上"上文/下文"是因为一句被检索出来的话常常脱离语境就看不懂——
// 那正是历史召回最容易失效的地方。
func renderHits(hits []history.Hit) string {
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n")
		}
		if !h.Item.At.IsZero() {
			b.WriteString(h.Item.At.Format("01-02 15:04"))
			b.WriteString(" ")
		}
		b.WriteString(speakerOf(h.Item.Kind))
		b.WriteString("：")
		b.WriteString(h.Item.Content)
		if s := strings.TrimSpace(h.Snippet); s != "" && strings.Contains(s, "[") {
			b.WriteString("\n    片段：")
			b.WriteString(s)
		}
		if h.Before != nil {
			b.WriteString("\n    上文：")
			b.WriteString(speakerOf(h.Before.Kind))
			b.WriteString("：")
			b.WriteString(truncateRunes(h.Before.Content, 60))
		}
		if h.After != nil {
			b.WriteString("\n    下文：")
			b.WriteString(speakerOf(h.After.Kind))
			b.WriteString("：")
			b.WriteString(truncateRunes(h.After.Content, 60))
		}
	}
	return b.String()
}

func speakerOf(k history.Kind) string {
	// 未识别的类型一律当对方：宁可标签不精确，也不要丢内容。
	//nolint:exhaustive // 见上：其余 Kind 共用同一个标签
	switch k {
	case history.KindAssistant, history.KindToolCall:
		return "我"
	case history.KindToolResult:
		return "工具"
	case history.KindMarker:
		return "内部"
	default:
		return "对方"
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (t recallHistory) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[recallHistoryArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if t.deps.History == nil {
		return tool.Failure("历史存储未配置"), nil
	}
	key := tool.ScopeFrom(ctx)
	if key == "" {
		return tool.Failure("无法确定当前会话，暂不能召回历史"), nil
	}

	items, err := t.deps.History.Messages(ctx, key)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	if limit > MaxRecallLimit {
		limit = MaxRecallLimit
	}
	needle := strings.ToLower(strings.TrimSpace(in.Query))

	// 有检索能力就走检索：能拿到片段与前后文，且不必把整段历史读进内存。
	if searcher, ok := t.deps.History.(HistorySearcher); ok {
		hits, err := searcher.Search(ctx, key, strings.TrimSpace(in.Query), limit)
		if err != nil {
			return tool.Failure(err.Error()), nil
		}
		if len(hits) == 0 {
			if needle == "" {
				return tool.Success("（还没有历史记录）"), nil
			}
			return tool.Success("（没有找到提到「" + in.Query + "」的历史）"), nil
		}
		return tool.Success(truncateOutput(renderHits(hits))), nil
	}

	var hits []string
	for _, it := range items {
		var speaker string
		// 只召回真正的对话轮次；工具轮次与 marker 都不属于"聊过的内容"。
		//nolint:exhaustive // 其余 Kind 一律走 default 跳过
		switch it.Kind {
		case history.KindUser:
			speaker = "对方"
		case history.KindAssistant:
			speaker = "我"
		default:
			// marker 是内部条目（例如记忆落盘），不是对话，不参与召回。
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(it.Content), needle) {
			continue
		}
		hits = append(hits, speaker+"："+it.Content)
	}

	if len(hits) == 0 {
		if needle == "" {
			return tool.Success("（还没有历史记录）"), nil
		}
		return tool.Success("（没有找到提到「" + in.Query + "」的历史）"), nil
	}
	if len(hits) > limit {
		hits = hits[len(hits)-limit:]
	}
	return tool.Success(truncateOutput(strings.Join(hits, "@BS@n"))), nil
}
