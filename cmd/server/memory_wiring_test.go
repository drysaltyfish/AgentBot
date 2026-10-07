package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// Test_F49_BuildAgentWiresTieredMemory 覆盖 F-49 的接线本身：
// 开启记忆后，组合根必须装配分层实现（而不是旧的扁平实现）。
//
// 断言类型而不是行为，是因为行为已由 internal/memory 的测试覆盖；
// 这里唯一要防的是"库写好了、组合根又忘了换"——那正是 HANDOFF 里重复出现的失败模式。
func Test_F49_BuildAgentWiresTieredMemory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Agent.Enabled = false
	cfg.Agent.Memory = ptr(true)

	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "tiered.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	asm := conversation.New(conversation.Options{System: "s"})
	lg := testLogger(t)
	_, mem, err := buildAgent(cfg, llm.NewEcho(""), asm, history.NewMemory(10), st, &callerBox{}, lg, nil, metrics.NewCatalog(metrics.CatalogOptions{}))
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
	tiered, ok := mem.(*memory.TieredMemory)
	if !ok {
		t.Fatalf("开启记忆后应装配分层实现，实际 %T", mem)
	}
	// 三层都要真的连上：写一条后 Working 立刻可见（这是分层的可观测特征）。
	scoped := agent.WithMemoryScope(ctx, "g")
	if err := tiered.Save(scoped, "确认接线成功"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	working, err := tiered.Working(scoped)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(working) != 1 || working[0].Text != "确认接线成功" {
		t.Fatalf("Working 层未生效: %+v", working)
	}
}
