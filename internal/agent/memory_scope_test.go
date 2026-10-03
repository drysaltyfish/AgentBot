package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// Test_F47_MemoryIsIsolatedByScope 是修复后的回归测试。
//
// 修复前 MemoryStore 是全局一份、Recall 不区分会话，任意会话存的内容任意会话都读得到；
// F-47 明确要求"群 A 的记忆不得出现在群 B 的回忆中"。
func Test_F47_MemoryIsIsolatedByScope(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	ctxA := WithMemoryScope(context.Background(), "group-111")
	ctxB := WithMemoryScope(context.Background(), "group-222")

	if err := mem.Save(ctxA, "群A的秘密"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	gotB, err := mem.Recall(ctxB)
	if err != nil {
		t.Fatalf("Recall B: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("作用域未隔离：群 B 读到了 %v", gotB)
	}

	gotA, _ := mem.Recall(ctxA)
	if len(gotA) != 1 || gotA[0] != "群A的秘密" {
		t.Fatalf("本作用域应读到自己的记忆: %v", gotA)
	}
	if mem.LenScope("group-111") != 1 || mem.LenScope("group-222") != 0 {
		t.Fatalf("按作用域计数不对")
	}
}

// Test_F47_MemoryIsolationEndToEnd 端到端确认注入也隔离。
func Test_F47_MemoryIsolationEndToEnd(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	groupA := session.Key{SelfID: 1, GroupID: 111}
	groupB := session.Key{SelfID: 1, GroupID: 222}

	r := tool.New()
	if err := RegisterVirtual(r, mem); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}

	// 在 A 里存一条。
	save := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", ActionSaveMemory, `{"text":"群A的秘密"}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "好", FinishReason: "stop"},
	}}
	aA := &ReactAgent{LLM: save, Tools: r, SystemPrompt: "s", Memory: mem}
	if _, err := aA.Run(context.Background(), Input{Query: "记住", SessionKey: groupA}); err != nil {
		t.Fatalf("Run A: %v", err)
	}
	if mem.Len() != 1 {
		t.Fatalf("A 的记忆未写入: %d", mem.Len())
	}

	// B 的提示词里不能出现它。
	probe := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	aB := &ReactAgent{LLM: probe, Tools: r, SystemPrompt: "s", Memory: mem}
	if _, err := aB.Run(context.Background(), Input{Query: "你好", SessionKey: groupB}); err != nil {
		t.Fatalf("Run B: %v", err)
	}
	for _, m := range probe.request(0).Messages {
		if strings.Contains(m.Content, "群A的秘密") {
			t.Fatalf("群 A 的记忆被注入到了群 B 的提示词：%q", m.Content)
		}
	}

	// 但 B 自己存的东西，B 能读到。
	probe2 := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", ActionSaveMemory, `{"text":"群B的偏好"}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "好", FinishReason: "stop"},
	}}
	aB2 := &ReactAgent{LLM: probe2, Tools: r, SystemPrompt: "s", Memory: mem}
	if _, err := aB2.Run(context.Background(), Input{Query: "记住", SessionKey: groupB}); err != nil {
		t.Fatalf("Run B2: %v", err)
	}
	items, _ := mem.Recall(WithMemoryScope(context.Background(), groupB.String()))
	if len(items) != 1 || !strings.Contains(items[0], "群B的偏好") {
		t.Fatalf("B 应读到自己的记忆: %v", items)
	}
}

// Test_F47_MemoryLimitMatchesSpec 守住长度上限与 F-47 一致（500 字符）。
func Test_F47_MemoryLimitMatchesSpec(t *testing.T) {
	t.Parallel()
	ctx := WithMemoryScope(context.Background(), "s")
	m := NewMemoryStore(0)
	if err := m.Save(ctx, strings.Repeat("字", MemoryLimit)); err != nil {
		t.Fatalf("恰好到上限应可保存: %v", err)
	}
	if err := m.Save(ctx, strings.Repeat("字", MemoryLimit+1)); !errors.Is(err, ErrMemoryTooLong) {
		t.Fatalf("超长应被拒: %v", err)
	}
	if MemoryLimit != 500 {
		t.Fatalf("上限应与 F-47 一致（500）: %d", MemoryLimit)
	}
}

// Test_F47_PersistentMemorySurvivesRestart 是本次要补的核心验收。
//
// 用两个独立的 HistoryMemory 实例模拟"重启"：第一个写入，第二个（同一路径）必须读到。
func Test_F47_PersistentMemorySurvivesRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	ctxA := WithMemoryScope(context.Background(), "group-111")

	first := NewHistoryMemory(history.NewFile(path, 64))
	if err := first.Save(ctxA, "主人喜欢橘子汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 模拟重启：全新的存储实例，同一个文件。
	second := NewHistoryMemory(history.NewFile(path, 64))
	got, err := second.Recall(ctxA)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0] != "主人喜欢橘子汁" {
		t.Fatalf("重启后记忆丢失: %v", got)
	}

	// 落盘后依然按作用域隔离。
	other, _ := second.Recall(WithMemoryScope(context.Background(), "group-222"))
	if len(other) != 0 {
		t.Fatalf("落盘实现也必须隔离作用域: %v", other)
	}
}

func Test_F47_PersistentMemoryValidatesAndDedupes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	m := NewHistoryMemory(history.NewFile(path, 64))
	ctx := WithMemoryScope(context.Background(), "s")

	if err := m.Save(ctx, "   "); !errors.Is(err, ErrEmptyMemory) {
		t.Fatalf("空记忆应被拒: %v", err)
	}
	if err := m.Save(ctx, "a\nb"); !errors.Is(err, ErrMultilineMemory) {
		t.Fatalf("多行记忆应被拒: %v", err)
	}
	if err := m.Save(ctx, strings.Repeat("字", MemoryLimit+1)); !errors.Is(err, ErrMemoryTooLong) {
		t.Fatalf("超长记忆应被拒: %v", err)
	}

	if err := m.Save(ctx, "同一条"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := m.Save(ctx, "同一条"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := m.Recall(ctx)
	if len(got) != 1 {
		t.Fatalf("重复写入应去重: %v", got)
	}
}

// Test_F47_PersistentMemoryWorksThroughVirtualAction 端到端：save_memory -> 重启 -> 注入。
func Test_F47_PersistentMemoryWorksThroughVirtualAction(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	key := session.Key{SelfID: 1, UserID: 100}
	mem1 := NewHistoryMemory(history.NewFile(path, 64))

	r := tool.New()
	if err := RegisterVirtual(r, mem1); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}
	save := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", ActionSaveMemory, `{"text":"喜欢橘子汁"}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "好", FinishReason: "stop"},
	}}
	a := &ReactAgent{LLM: save, Tools: r, SystemPrompt: "s", Memory: mem1}
	if _, err := a.Run(context.Background(), Input{Query: "记住", SessionKey: key}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 重启后：新的存储实例 + 新的 Agent，记忆必须出现在提示词里。
	mem2 := NewHistoryMemory(history.NewFile(path, 64))
	r2 := tool.New()
	if err := RegisterVirtual(r2, mem2); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}
	probe := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	b := &ReactAgent{LLM: probe, Tools: r2, SystemPrompt: "s", Memory: mem2}
	if _, err := b.Run(context.Background(), Input{Query: "我喜欢什么", SessionKey: key}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, m := range probe.request(0).Messages {
		if strings.Contains(m.Content, "喜欢橘子汁") {
			found = true
		}
	}
	if !found {
		t.Fatalf("重启后记忆未注入提示词: %+v", probe.request(0).Messages)
	}
}

func Test_F47_NoMemoryStoreFailsLoudly(t *testing.T) {
	t.Parallel()
	var m *HistoryMemory
	if err := m.Save(context.Background(), "x"); !errors.Is(err, ErrMemoryUnavailable) {
		t.Fatalf("未配置存储应明确失败: %v", err)
	}
	if _, err := m.Recall(context.Background()); !errors.Is(err, ErrMemoryUnavailable) {
		t.Fatalf("未配置存储应明确失败: %v", err)
	}
}
