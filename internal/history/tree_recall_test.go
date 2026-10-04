package history

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/memory"
)

// noKeywordNoVector 把 F-51 的两路都关掉，用来证明摘要树确实是一条独立召回源。
func noKeywordNoVector() memory.HybridConfig {
	return memory.HybridConfig{KeywordWeight: -1, VectorWeight: -1, TopK: 5, CandidateK: 10}
}

// Test_F52_TreeIsAnIndependentRecallSource 是 F-52 接线的核心断言：
// 把关键词与向量两路都关掉后，摘要树仍必须能命中——否则它只是"多一层转发"。
func Test_F52_TreeIsAnIndependentRecallSource(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k",
		Item{Kind: KindUser, Content: "今天想喝点橙汁"},
		Item{Kind: KindAssistant, Content: "那就买一箱橙子"},
		Item{Kind: KindUser, Content: "顺便把作业交了"},
	)
	h := NewHybrid(base, noKeywordNoVector()).WithTree(&TreeConfig{}, nil)

	hits, err := h.Search(context.Background(), "k", "橙汁", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("两路权重都关掉时，摘要树应仍能命中")
	}
	// 树会同时返回上层摘要节点与叶子节点：前者带出宏观上下文，后者是原文。
	// 这里只断言"期望的条目在里面"，且每条都带摘要片段标记。
	found := false
	for _, hit := range hits {
		if hit.Item.Content == "今天想喝点橙汁" {
			found = true
		}
		if hit.Snippet == "" || hit.Snippet[0] != '[' {
			t.Fatalf("树的命中应带摘要片段标记: %q", hit.Snippet)
		}
	}
	if !found {
		t.Fatalf("应命中包含关键词的条目: %+v", hits)
	}
}

// Test_F52_TreeRebuildsWhenHistoryGrows 钉住缓存失效：
// 追加式历史里条目数是唯一的"树过期"信号，漏掉它会永远搜不到新内容。
func Test_F52_TreeRebuildsWhenHistoryGrows(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k", Item{Kind: KindUser, Content: "今天想喝点橙汁"})
	h := NewHybrid(base, noKeywordNoVector()).WithTree(&TreeConfig{}, nil)
	ctx := context.Background()

	if hits, _ := h.Search(ctx, "k", "橙汁", 5); len(hits) == 0 {
		t.Fatal("首次检索应命中")
	}
	appendAll(t, base, "k", Item{Kind: KindUser, Content: "再买点蓝莓"})

	hits, err := h.Search(ctx, "k", "蓝莓", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, hit := range hits {
		if hit.Item.Content == "再买点蓝莓" {
			found = true
		}
	}
	if !found {
		t.Fatalf("追加后应能命中新内容: %+v", hits)
	}
}

// Test_F52_TreeDisabledLeavesOtherPathsAlone 钉住开关：没开树时，
// 两路权重关掉就必须什么都没有——否则说明有别的来源在偷偷兜底。
func Test_F52_TreeDisabledLeavesOtherPathsAlone(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k", Item{Kind: KindUser, Content: "今天想喝点橙汁"})
	h := NewHybrid(base, noKeywordNoVector())

	hits, err := h.Search(context.Background(), "k", "橙汁", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("未启用树时不应有命中: %+v", hits)
	}
}
