package history

import (
	"context"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/vector"
)

// TreeConfig 是摘要树召回（F-52）的配置；零值即规格默认值。
type TreeConfig struct {
	// MaxLevels/MinCluster/Branching/ClusterThreshold 传给 memory.TreeOptions。
	MaxLevels        int
	MinCluster       int
	Branching        int
	ClusterThreshold float64
	// MaxNodes 是全树节点上限；<=0 用 memory 默认值。
	MaxNodes int
}

// treeEmbedder 用 F-50 的确定性二值哈希向量的前身（浮点特征哈希）做聚类。
//
// 不引 embedding 依赖：F-52 允许 embedding 失败时跳过该簇，而确定性哈希
// 至少保证"同文本同向量"，让聚类结果可复现。
type treeEmbedder struct{}

// Embed 实现 memory.Embedder。
func (treeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return vector.TextVector(text, vector.TextDim), nil
}

// treeCache 是一棵会话的摘要树快照。
type treeCache struct {
	// count 是构建时的条目数；条目数变了就重建（追加式历史只有这一种变化）。
	count int
	tree  *memory.SummaryTree
}

// summaryHits 用摘要树检索当前会话，返回命中的条目下标（按树的名次）。
//
// 树是**独立的一条召回源**：两路权重都关掉时它仍能命中，因为它的检索发生在
// 摘要层而不是原文层——那正是"宏观问题命中上层摘要"的落点。
func (h *Hybrid) summaryHits(ctx context.Context, key string, items []Item, entries []hybridEntry, query string, k int) []Hit {
	if h.Tree == nil || len(entries) == 0 {
		return nil
	}
	tree := h.ensureTree(ctx, key, items, entries)
	if tree == nil {
		return nil
	}
	hits := tree.Search(query, k)
	if len(hits) == 0 {
		return nil
	}
	out := make([]Hit, 0, len(hits))
	for _, th := range hits {
		node, ok := tree.Node(th.ID)
		if !ok {
			continue
		}
		ref := firstRef(node.Refs)
		if ref < 0 || ref >= len(entries) {
			continue
		}
		e := entries[ref]
		snippet := strings.TrimSpace(th.Text)
		if snippet == "" {
			snippet = node.IndexText()
		}
		out = append(out, Hit{
			Item: e.item,
			// 片段用方括号包起来：renderHits 只对带 [ ] 的片段做展示，
			// 而不带标记的文本已经就是正文本身。
			Snippet: "[" + clipRunes(snippet, 120) + "]",
			Before:  neighbour(items, e.orig-1),
			After:   neighbour(items, e.orig+1),
		})
	}
	return out
}

// ensureTree 返回会话当前的摘要树，必要时重建。
func (h *Hybrid) ensureTree(ctx context.Context, key string, items []Item, entries []hybridEntry) *memory.SummaryTree {
	h.treeMu.Lock()
	defer h.treeMu.Unlock()

	if c, ok := h.trees[key]; ok && c != nil && c.count == len(items) {
		return c.tree
	}
	chunks := make([]memory.Chunk, 0, len(entries))
	for i, e := range entries {
		chunks = append(chunks, memory.Chunk{
			Text: e.item.Content,
			Refs: []string{strconv.Itoa(i)},
		})
	}
	tree := memory.NewSummaryTree(memory.TreeOptions{
		MaxLevels:        h.Tree.MaxLevels,
		MinCluster:       h.Tree.MinCluster,
		Branching:        h.Tree.Branching,
		MaxNodes:         h.Tree.MaxNodes,
		ClusterThreshold: h.Tree.ClusterThreshold,
		// 摘要必须是确定性的：树在检索路径上按需重建，注入模型会让一次
		// recall_history 变成一次付费调用，而且结果不可复现。
		Summarizer: memory.JoinSummarizer{},
		Embedder:   treeEmbedder{},
		Warn:       h.warn,
	})
	// 构建用脱离取消的 ctx：调用方的 ctx 只约束本次检索，
	// 而树一旦建成会被后续检索复用，半途取消留着更糟。
	tree.Build(context.WithoutCancel(ctx), chunks)
	if h.trees == nil {
		h.trees = map[string]*treeCache{}
	}
	h.trees[key] = &treeCache{count: len(items), tree: tree}
	return tree
}

// clipRunes 按 rune 截断，避免把多字节字符切成半个。
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// firstRef 取节点引用的第一个条目下标；没有可用引用时返回 -1。
func firstRef(refs []string) int {
	for _, ref := range refs {
		if n, err := strconv.Atoi(strings.TrimSpace(ref)); err == nil {
			return n
		}
	}
	return -1
}
