package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

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
