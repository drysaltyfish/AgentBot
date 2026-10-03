package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

func Test_F40_RegisterVirtual(t *testing.T) {
	t.Parallel()
	r := tool.New()
	if err := RegisterVirtual(r, NewMemoryStore(0)); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}
	for _, name := range []string{ActionEndTurn, ActionSaveMemory, ActionNoop} {
		got, ok := r.Get(name)
		if !ok {
			t.Fatalf("虚拟动作 %s 未注册", name)
		}
		if !IsVirtual(got) {
			t.Fatalf("虚拟动作 %s 必须标记为 Virtual", name)
		}
	}
}

// Test_F40_EndActionTerminatesWithoutReply 是 F-40 的验收点。
func Test_F40_EndActionTerminatesWithoutReply(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", ActionEndTurn, "{}")}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "这句不应该出现", FinishReason: "stop"},
	}}
	r := tool.New()
	if err := RegisterVirtual(r, NewMemoryStore(0)); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}
	a := &ReactAgent{LLM: fake, Tools: r}

	out, err := a.Run(context.Background(), Input{Query: "别理我"})
	if !errors.Is(err, ErrEndOfTurn) {
		t.Fatalf("end_action 应返回 ErrEndOfTurn: %v", err)
	}
	if out.FinishReason != FinishReasonEndOfTurn {
		t.Fatalf("finish reason: %q", out.FinishReason)
	}
	if out.Text != "" {
		t.Fatalf("结束本轮时不应产生回复文本: %q", out.Text)
	}
	if fake.count() != 1 {
		t.Fatalf("结束本轮后不应再调用 LLM: %d", fake.count())
	}
}

// Test_F40_SaveMemoryLandsInNextRunPrompt 覆盖"写入后在下次会话的提示词中出现"。
func Test_F40_SaveMemoryLandsInNextRunPrompt(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	r := tool.New()
	if err := RegisterVirtual(r, mem); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}

	// 第一次运行：让模型保存一条记忆。
	save := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", ActionSaveMemory, `{"text":"主人喜欢橘子味"}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "记住啦", FinishReason: "stop"},
	}}
	a := &ReactAgent{LLM: save, Tools: r, SystemPrompt: "你是香橙娘", Memory: mem}
	if _, err := a.Run(context.Background(), Input{Query: "记住我喜欢橘子味"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mem.Len() != 1 {
		t.Fatalf("记忆未写入: %d", mem.Len())
	}

	// 第二次运行：记忆必须出现在提示词里。
	second := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "嗯嗯", FinishReason: "stop"}}}
	b := &ReactAgent{LLM: second, Tools: r, SystemPrompt: "你是香橙娘", Memory: mem}
	if _, err := b.Run(context.Background(), Input{Query: "我喜欢什么味"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, m := range second.request(0).Messages {
		if strings.Contains(m.Content, "主人喜欢橘子味") {
			found = true
		}
	}
	if !found {
		t.Fatalf("记忆未注入下一次会话的提示词: %+v", second.request(0).Messages)
	}
}

// Test_F40_MemoryInjectionPosition 守住 ADR-0002 的位置约定。
func Test_F40_MemoryInjectionPosition(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	if err := mem.Save(context.Background(), "记忆A"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fake := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	a := &ReactAgent{
		LLM: fake, Tools: tool.New(), SystemPrompt: "系统提示词", Memory: mem,
	}
	if _, err := a.Run(context.Background(), Input{
		Query:   "当前问题",
		History: []llm.Message{{Role: llm.RoleUser, Content: "历史1"}, {Role: llm.RoleAssistant, Content: "历史2"}},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := fake.request(0).Messages
	// 期望顺序：system 提示词 → 记忆 → 历史 → 当前输入
	if len(msgs) != 5 {
		t.Fatalf("消息数: actual=%d expected=5（%+v）", len(msgs), msgs)
	}
	if msgs[0].Content != "系统提示词" {
		t.Fatalf("第 0 条应是 system 提示词: %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[1].Content, "记忆A") {
		t.Fatalf("第 1 条应是记忆块（system 之后、历史之前）: %q", msgs[1].Content)
	}
	if msgs[2].Content != "历史1" || msgs[3].Content != "历史2" {
		t.Fatalf("历史顺序被破坏: %+v", msgs)
	}
	if msgs[4].Content != "当前问题" {
		t.Fatalf("当前输入必须在最后: %q", msgs[4].Content)
	}
}

// Test_F40_SaveMemoryValidationIsFedBack 覆盖"校验失败回灌而非静默丢弃"。
func Test_F40_SaveMemoryValidationIsFedBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{"空记忆", "   "},
		{"多行记忆", "第一行\n第二行"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mem := NewMemoryStore(0)
			r := tool.New()
			if err := RegisterVirtual(r, mem); err != nil {
				t.Fatalf("RegisterVirtual: %v", err)
			}
			args, _ := json.Marshal(map[string]string{"text": tc.text})
			fake := &scriptedLLM{replies: []*llm.ChatResponse{
				{ToolCalls: []llm.ToolCall{toolCall("c1", ActionSaveMemory, string(args))}, FinishReason: llm.FinishReasonToolCalls},
				{Content: "好吧", FinishReason: "stop"},
			}}
			a := &ReactAgent{LLM: fake, Tools: r, Memory: mem}
			if _, err := a.Run(context.Background(), Input{Query: "记住"}); err != nil {
				t.Fatalf("校验失败不应中断循环: %v", err)
			}
			if mem.Len() != 0 {
				t.Fatalf("非法记忆不应被写入: %d", mem.Len())
			}
			msgs := fake.request(1).Messages
			last := msgs[len(msgs)-1]
			if last.Role != llm.RoleTool || last.Content == "" {
				t.Fatalf("校验失败必须作为 observation 回灌: %+v", last)
			}
		})
	}
}

func Test_F40_NoopSucceeds(t *testing.T) {
	t.Parallel()
	n := noopTool{}
	res, err := n.Execute(context.Background(), json.RawMessage("{}"))
	if err != nil || res.Failed() {
		t.Fatalf("noop 应成功: %+v %v", res, err)
	}
	if !IsVirtual(n) {
		t.Fatalf("noop 必须标记为虚拟动作")
	}
}

func Test_F40_SaveMemoryWithoutStoreFailsLoudly(t *testing.T) {
	t.Parallel()
	s := saveMemoryTool{mem: nil}
	res, err := s.Execute(context.Background(), json.RawMessage(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("不应返回 error（要回灌）: %v", err)
	}
	if !res.Failed() {
		t.Fatalf("未配置记忆时必须明确失败，而不是让模型以为写成功了")
	}
}

func Test_F40_MemoryStoreValidationAndBoundedness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := NewMemoryStore(3)

	if err := m.Save(ctx, "   "); !errors.Is(err, ErrEmptyMemory) {
		t.Fatalf("空记忆应被拒: %v", err)
	}
	if err := m.Save(ctx, "a\nb"); !errors.Is(err, ErrMultilineMemory) {
		t.Fatalf("多行记忆应被拒: %v", err)
	}
	if err := m.Save(ctx, strings.Repeat("字", MemoryLimit+1)); !errors.Is(err, ErrMemoryTooLong) {
		t.Fatalf("超长记忆应被拒: %v", err)
	}

	for _, s := range []string{"一", "二", "三", "四"} {
		if err := m.Save(ctx, s); err != nil {
			t.Fatalf("Save(%s): %v", s, err)
		}
	}
	if m.Len() != 3 {
		t.Fatalf("记忆必须有界: %d", m.Len())
	}
	items, _ := m.Recall(ctx)
	if strings.Join(items, ",") != "二,三,四" {
		t.Fatalf("超限应丢弃最旧的: %v", items)
	}

	// 去重：重复写入不改变结果，保证同样的写入序列得到同样的渲染。
	if err := m.Save(ctx, "三"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, _ := m.Recall(ctx)
	if strings.Join(again, ",") != strings.Join(items, ",") {
		t.Fatalf("重复写入不应改变记忆顺序: %v", again)
	}
}

func Test_F40_RenderMemoryIsDeterministic(t *testing.T) {
	t.Parallel()
	items := []string{"甲", "乙", "丙"}
	first := RenderMemory(items)
	for i := 0; i < 5; i++ {
		if again := RenderMemory(items); again != first {
			t.Fatalf("渲染必须确定性: %q vs %q", first, again)
		}
	}
	if RenderMemory(nil) != "" {
		t.Fatalf("无记忆时不应产生消息块")
	}
	if !strings.Contains(first, "甲") || !strings.Contains(first, "丙") {
		t.Fatalf("渲染应包含全部记忆: %q", first)
	}
}
