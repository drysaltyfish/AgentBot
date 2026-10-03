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
