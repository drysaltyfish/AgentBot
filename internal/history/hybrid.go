package history

import (
	"context"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/vector"
)

// Hybrid 在既有历史存储之上叠加混合检索（F-51）：BM25 关键词一路 + 二值向量一路，
// 用 RRF 融合后返回。
//
// 它嵌入 History，因此**全部既有方法原样透传**；额外实现 Searcher，
// 于是 recall_history 一旦发现它就会自动改走检索路径（F-84 的既有约定），
// 不需要工具侧知道"这次用的是哪一路"。
//
// 为什么关键词路用内存 BM25 而不是底层 FTS：两路必须对**同一份条目**排名，
// 融合结果才能用同一个 id 映射回去。接 FTS 就得靠"内容+时间"反查条目，
// 一旦有重复内容就会错位——而错位是静默的。
type Hybrid struct {
	History
	// Config 是 F-51 的融合参数（权重、TopK、候选数、RRF k）。
	Config memory.HybridConfig
}

// NewHybrid 构造；base 为 nil 时退化为不可用（Search 返回错误）。
func NewHybrid(base History, cfg memory.HybridConfig) *Hybrid {
	return &Hybrid{History: base, Config: cfg}
}

// hybridEntry 记住条目在原始切片中的位置，用于带出前后文。
type hybridEntry struct {
	orig int
	item Item
}

// Search 实现 Searcher。
//
// limit 优先于 Config.TopK：工具侧的限制是硬上限，配置只提供默认值。
func (h *Hybrid) Search(ctx context.Context, key, query string, limit int) ([]Hit, error) {
	if h == nil || h.History == nil {
		return nil, memory.ErrUnavailable
	}
	if limit <= 0 {
		limit = memory.DefaultHybridTopK
	}
	items, err := h.History.Messages(ctx, key)
	if err != nil {
		return nil, err
	}
	entries := conversationalEntries(items)
	if len(entries) == 0 {
		return nil, nil
	}
	// 空查询：两路都给不出有意义的名次，直接给最近 limit 条——那正是"留空取最近几条"的语义。
	if strings.TrimSpace(query) == "" {
		return recentHits(items, entries, limit), nil
	}

	cfg := h.Config
	cfg.TopK = limit
	if cfg.CandidateK < limit {
		cfg.CandidateK = limit
	}
	bm := memory.NewBM25Index()
	idx := vector.NewIndex()
	for i, e := range entries {
		id := int64(i)
		bm.Add(id, e.item.Content)
		idx.Add(id, e.item.Content, vector.TextVector(e.item.Content, vector.TextDim))
	}
	retriever := memory.NewHybridRetrieverWithSearchers(bm, memory.VectorIndexSearcher{Index: idx}, cfg)
	fused, _ := retriever.Search(ctx, query, vector.TextVector(query, vector.TextDim), limit)

	out := make([]Hit, 0, len(fused))
	for _, f := range fused {
		if f.ID < 0 || int(f.ID) >= len(entries) {
			continue
		}
		e := entries[f.ID]
		out = append(out, Hit{
			Item:   e.item,
			Before: neighbour(items, e.orig-1),
			After:  neighbour(items, e.orig+1),
		})
	}
	return out, nil
}

// conversationalEntries 只保留真正的对话轮次：工具轮次与 marker 不是"聊过的内容"。
func conversationalEntries(items []Item) []hybridEntry {
	out := make([]hybridEntry, 0, len(items))
	for i, it := range items {
		switch it.Kind {
		case KindUser, KindAssistant:
			out = append(out, hybridEntry{orig: i, item: it})
		default:
		}
	}
	return out
}

// recentHits 返回最近 limit 条对话（按原顺序）。
func recentHits(items []Item, entries []hybridEntry, limit int) []Hit {
	start := 0
	if len(entries) > limit {
		start = len(entries) - limit
	}
	out := make([]Hit, 0, len(entries)-start)
	for _, e := range entries[start:] {
		out = append(out, Hit{
			Item:   e.item,
			Before: neighbour(items, e.orig-1),
			After:  neighbour(items, e.orig+1),
		})
	}
	return out
}

// neighbour 返回相邻条目；越界时返回 nil（渲染层据此省略前后文）。
func neighbour(items []Item, i int) *Item {
	if i < 0 || i >= len(items) {
		return nil
	}
	cp := items[i].Clone()
	return &cp
}

var _ Searcher = (*Hybrid)(nil)
