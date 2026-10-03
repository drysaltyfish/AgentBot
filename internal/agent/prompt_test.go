package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

func Test_ProactiveMemory_ComposesInstructionAtTheEnd(t *testing.T) {
	t.Parallel()
	base := "你是香橙娘。"
	got := ComposeSystemPrompt(base, "记住重要事实。")

	if !strings.HasPrefix(got, base) {
		t.Fatalf("人格部分必须原样保持开头（它是不可变前缀的起点）: %q", got)
	}
	if !strings.HasSuffix(got, "记住重要事实。") {
		t.Fatalf("指令应追加在末尾: %q", got)
	}
	if strings.Count(got, "记住重要事实。") != 1 {
		t.Fatalf("指令只应出现一次: %q", got)
	}
}

func Test_ProactiveMemory_StabilityMatters(t *testing.T) {
	t.Parallel()
	// 同样的输入必须得到逐字节相同的结果——否则前缀缓存每轮都会失效。
	base := "你是香橙娘。\n"
	a := ComposeSystemPrompt(base, DefaultProactiveMemoryInstruction)
	b := ComposeSystemPrompt(base, DefaultProactiveMemoryInstruction)
	if a != b {
		t.Fatalf("同样的输入必须产生相同的前缀")
	}
	// 末尾空白应被规整，避免"看起来一样但字节不同"。
	if strings.Contains(a, "\n\n\n") {
		t.Fatalf("不应留下多余空行: %q", a)
	}
}

func Test_ProactiveMemory_EmptyInstructionKeepsBase(t *testing.T) {
	t.Parallel()
	base := "你是香橙娘。"
	if got := ComposeSystemPrompt(base, "   "); got != base {
		t.Fatalf("空指令不应改动提示词: %q", got)
	}
	if got := ComposeSystemPrompt(base, ""); got != base {
		t.Fatalf("空指令不应改动提示词: %q", got)
	}
}

func Test_ProactiveMemory_CustomOverridesDefault(t *testing.T) {
	t.Parallel()
	if got := ProactiveMemoryInstruction("  自定义指令  "); got != "自定义指令" {
		t.Fatalf("自定义指令应被 trim 后返回: %q", got)
	}
	if got := ProactiveMemoryInstruction(""); got != DefaultProactiveMemoryInstruction {
		t.Fatalf("空时应回退到内置指令")
	}
	// 内置指令必须真的表达了"该记什么/不该记什么"以及"一次一条"。
	low := DefaultProactiveMemoryInstruction
	for _, want := range []string{"save_memory", "一次只记一条", "不要记", "不要重复记"} {
		if !strings.Contains(low, want) {
			t.Fatalf("内置指令应包含 %q: %q", want, low)
		}
	}
}

// Test_F89_AgentReportsPromptAndMemoryDigests 覆盖**装配层接线**。
//
// 这条测试是补的：我先前只测了存储层的分类逻辑（直接传指纹进去），
// 于是 MemoryDigest 在 ReactAgent 里**从未被赋值**也没被发现——
// 记忆变更因此一直被误报为"意外前缀分歧"。真机日志才暴露它。
//
// 教训：跨层的"字段有没有被填"必须端到端测，只测下游逻辑会漏掉接线。
func Test_F89_AgentReportsPromptAndMemoryDigests(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	key := session.Key{SelfID: 1, UserID: 100}
	ctx := context.Background()

	if err := mem.Save(WithMemoryScope(ctx, key.String()), "喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fake := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	a := &ReactAgent{LLM: fake, Tools: tool.New(), SystemPrompt: "系统提示词", Memory: mem}

	out, err := a.Run(ctx, Input{Query: "你好", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.PromptDigest) == 0 {
		t.Fatalf("PromptDigest 必须被填充（否则前缀稳定性检查形同虚设）")
	}
	if out.MemoryDigest == "" {
		t.Fatalf("MemoryDigest 必须被填充——为空会让记忆变更被误报成意外分歧")
	}
	first := out.MemoryDigest

	// 记忆变化后，记忆块指纹必须随之变化。
	if err := mem.Save(WithMemoryScope(ctx, key.String()), "喜欢看动漫"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second := &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	a.LLM = second
	out2, err := a.Run(ctx, Input{Query: "再说一次", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out2.MemoryDigest == first {
		t.Fatalf("记忆变化后指纹必须变化，否则分类器区分不了预期与意外")
	}
	// 没有记忆时不应误报有记忆块。
	noMem := &ReactAgent{LLM: second, Tools: tool.New(), SystemPrompt: "S"}
	out3, err := noMem.Run(ctx, Input{Query: "hi", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out3.MemoryDigest != "" {
		t.Fatalf("无记忆时 MemoryDigest 应为空: %q", out3.MemoryDigest)
	}
}
