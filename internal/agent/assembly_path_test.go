package agent

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// Test_AssemblyIsTheOnlyPath 是回归测试：agent 真正发出去的请求必须与装配器的
// 输出逐条相同。
//
// 为什么值得单独测：agent 曾经自己拼消息，于是装配器里配置的呈现窗口与环境消息
// token 预算只活在测试里，线上永远不生效，而日志里的 prefix_hash 记的还是那份
// 没人用的前缀副本。这条断言把"布局只有一份实现"钉住。
func Test_AssemblyIsTheOnlyPath(t *testing.T) {
	t.Parallel()
	asm := conversation.New(conversation.Options{
		System:             "S",
		MaxHistory:         2,
		AmbientTokenBudget: 40,
		AmbientMaxChars:    8,
	})
	fake := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	a := &ReactAgent{LLM: fake, Tools: tool.New(), Assembler: asm}

	items := []history.Item{
		{Kind: history.KindUser, Content: "对话一"},
		{Kind: history.KindUser, Content: "对话二"},
		{Kind: history.KindUser, Content: "对话三"},
		{Kind: history.KindUser, Content: "环境一", Ambient: true},
		{Kind: history.KindUser, Content: "环境二", Ambient: true},
	}
	if _, err := a.Run(context.Background(), Input{
		Query:      "现在",
		SessionKey: session.Key{GroupID: 1},
		History:    items,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fake.request(0).Messages
	want := asm.Build(items, "", "现在")
	if len(got) != len(want) {
		t.Fatalf("实际请求与装配器输出长度不同: actual=%d want=%d; actual=%+v; want=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("第 %d 条不一致: actual=(%s,%q) want=(%s,%q)", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}
	// 首条必须是装配器的不可变前缀：prefix_hash 记的就是它。
	if got[0].Content != asm.Prefix() {
		t.Fatalf("首条不是装配器前缀: %q", got[0].Content)
	}
	// 窗口与环境压缩确实生效：否则条目会被原样全部回灌。
	if len(got) >= len(items)+2 {
		t.Fatalf("窗口/环境压缩未生效，条目被原样回灌: %d 条", len(got))
	}
}
