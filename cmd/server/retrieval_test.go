package main

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
)

// Test_F51_WrapHistoryWithRetrievalFollowsConfig 覆盖 F-51 的接线开关：
// 关闭时原样返回（不改变召回行为），开启时套上实现了 Searcher 的包装层。
func Test_F51_WrapHistoryWithRetrievalFollowsConfig(t *testing.T) {
	t.Parallel()
	base := history.NewMemory(10)
	if err := base.Append(context.Background(), "k", history.Item{Kind: history.KindUser, Content: "你好"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	cfg := config.Default()
	if got := wrapHistoryWithRetrieval(cfg, base, nil); got != history.History(base) {
		t.Fatalf("默认关闭时应原样返回，实际 %T", got)
	}

	cfg.Retrieval.Enabled = ptr(true)
	got := wrapHistoryWithRetrieval(cfg, base, nil)
	hybrid, ok := got.(*history.Hybrid)
	if !ok {
		t.Fatalf("开启后应返回混合检索包装，实际 %T", got)
	}
	if _, ok := interface{}(hybrid).(history.Searcher); !ok {
		t.Fatal("包装层必须实现 Searcher，否则 recall_history 不会改走检索")
	}
	// 透传：包装层读到的必须是底层同一份数据。
	items, err := hybrid.Messages(context.Background(), "k")
	if err != nil || len(items) != 1 {
		t.Fatalf("包装层未透传底层存储: %+v (%v)", items, err)
	}
}
