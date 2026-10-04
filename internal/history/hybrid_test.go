package history

import (
	"context"
	"testing"
)

// appendAll 按顺序写入历史。
func appendAll(t *testing.T, h History, key string, items ...Item) {
	t.Helper()
	for _, it := range items {
		if err := h.Append(context.Background(), key, it); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// Test_F51_HybridSearchFindsKeywordHitWithContext 覆盖接线后的检索效果：
// 专有名词查询要能命中，并且带出前后文（renderHits 依赖它）。
func Test_F51_HybridSearchFindsKeywordHitWithContext(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k",
		Item{Kind: KindUser, Content: "上线前先跑一遍检查"},
		Item{Kind: KindAssistant, Content: "可以，用 deploy.sh 那条命令"},
		Item{Kind: KindUser, Content: "好的"},
	)
	h := NewHybrid(base, HybridConfigForTest())
	ctx := context.Background()

	hits, err := h.Search(ctx, "k", "deploy.sh", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("专有名词查询应当命中")
	}
	if hits[0].Item.Content != "可以，用 deploy.sh 那条命令" {
		t.Fatalf("命中条目不对: %+v", hits[0].Item)
	}
	if hits[0].Before == nil || hits[0].Before.Content != "上线前先跑一遍检查" {
		t.Fatalf("应带出上文: %+v", hits[0].Before)
	}
	if hits[0].After == nil || hits[0].After.Content != "好的" {
		t.Fatalf("应带出下文: %+v", hits[0].After)
	}
}

// Test_F51_HybridSkipsNonConversationalEntries 钉住"只召回聊过的内容"：
// 工具轮次与内部 marker 不该出现在召回里，即使它们的文本命中了查询。
func Test_F51_HybridSkipsNonConversationalEntries(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k",
		Item{Kind: KindUser, Content: "记住 deploy.sh 这个脚本"},
		Item{Kind: KindToolResult, Content: "deploy.sh 输出：成功"},
		Item{Kind: KindMarker, Content: "deploy.sh marker"},
	)
	h := NewHybrid(base, HybridConfigForTest())

	hits, err := h.Search(context.Background(), "k", "deploy.sh", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Item.Kind != KindUser {
		t.Fatalf("只应召回对话轮次，实际 %+v", hits)
	}
}

// Test_F51_HybridEmptyQueryReturnsRecent 覆盖工具的"留空取最近几条"语义。
func Test_F51_HybridEmptyQueryReturnsRecent(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	appendAll(t, base, "k",
		Item{Kind: KindUser, Content: "第一句"},
		Item{Kind: KindAssistant, Content: "第二句"},
		Item{Kind: KindUser, Content: "第三句"},
	)
	h := NewHybrid(base, HybridConfigForTest())

	hits, err := h.Search(context.Background(), "k", "  ", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("应返回最近 2 条，实际 %d", len(hits))
	}
	if hits[0].Item.Content != "第二句" || hits[1].Item.Content != "第三句" {
		t.Fatalf("最近几条应按原顺序: %+v", hits)
	}
}

// Test_F51_HybridDelegatesEverythingElse 说明包装是透明的：
// 追加、读取、裁剪都必须原样落到被包裹的存储上，否则"包一层"就会丢数据。
func Test_F51_HybridDelegatesEverythingElse(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	h := NewHybrid(base, HybridConfigForTest())
	ctx := context.Background()

	if err := h.Append(ctx, "k", Item{Kind: KindUser, Content: "写入"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	items, err := h.Messages(ctx, "k")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 1 || items[0].Content != "写入" {
		t.Fatalf("包装层没有透传: %+v", items)
	}
}

// Test_F51_HybridRespectsLimit 钉住 limit 是硬上限。
func Test_F51_HybridRespectsLimit(t *testing.T) {
	t.Parallel()
	base := NewMemory(100)
	for i := 0; i < 6; i++ {
		appendAll(t, base, "k", Item{Kind: KindUser, Content: "关于检索的第 " + string(rune('0'+i)) + " 条"})
	}
	h := NewHybrid(base, HybridConfigForTest())
	hits, err := h.Search(context.Background(), "k", "检索", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) > 2 {
		t.Fatalf("limit=2 时返回了 %d 条", len(hits))
	}
}
