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
	a := &ReactAgent{LLM: fake, Tools: tool.New(), Assembler: testAssembler("系统提示词"), Memory: mem}

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
	noMem := &ReactAgent{LLM: second, Tools: tool.New(), Assembler: testAssembler("S")}
	out3, err := noMem.Run(ctx, Input{Query: "hi", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out3.MemoryDigest != "" {
		t.Fatalf("无记忆时 MemoryDigest 应为空: %q", out3.MemoryDigest)
	}
}

// streamOnceLLM 只用于流式路径：吐一个分片后正常结束。
type streamOnceLLM struct{}

func (streamOnceLLM) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (streamOnceLLM) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 1)
	ch <- llm.Chunk{Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

// Test_F89_DirectAgentAlsoReportsMemoryDigest 钉住**直连路径**的同一处接线。
//
// 上面那条测试只覆盖了 ReactAgent，于是同一个字段在 DirectAgent（agent.enabled=false）
// 里漏填了很久也没被发现：直连路径下 MemoryDigest 恒为空串，
// store/prompt.go 的 compareDigest 因此永远判不出"记忆变更"，
// 每一次写入记忆都会触发一次"前缀意外分歧"告警——告警常响等于没有告警。
//
// 两个范式都必须填，所以两个范式都要测。
func Test_F89_DirectAgentAlsoReportsMemoryDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := session.Key{SelfID: 1, UserID: 100}
	mem := NewMemoryStore(0)
	if err := mem.Save(WithMemoryScope(ctx, key.String()), "喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	a := &DirectAgent{LLM: &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}, Assembler: testAssembler("系统提示词"), Memory: mem}
	out, err := a.Run(ctx, Input{Query: "你好", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.PromptDigest) == 0 {
		t.Fatalf("PromptDigest 必须被填充")
	}
	if out.MemoryDigest == "" {
		t.Fatalf("DirectAgent 的 MemoryDigest 必须被填充——为空会让记忆变更被误报成意外分歧")
	}
	first := out.MemoryDigest

	// 记忆变化后指纹必须随之变化。
	if err := mem.Save(WithMemoryScope(ctx, key.String()), "喜欢看动漫"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a.LLM = &scriptedLLM{replies: []*llm.ChatResponse{{Content: "ok", FinishReason: "stop"}}}
	out2, err := a.Run(ctx, Input{Query: "再说一次", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out2.MemoryDigest == first {
		t.Fatalf("记忆变化后指纹必须变化")
	}

	// 流式路径与整段路径共用同一个装配点，指纹也必须一致。
	as := &DirectAgent{LLM: streamOnceLLM{}, Assembler: testAssembler("系统提示词"), Memory: mem}
	sout, err := as.RunStream(ctx, Input{Query: "再再说一次", SessionKey: key}, llm.NewStreamSplitter(llm.StreamConfig{}))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if sout.MemoryDigest == "" {
		t.Fatalf("流式路径同样必须填 MemoryDigest")
	}
	if sout.MemoryDigest != out2.MemoryDigest {
		t.Fatalf("同一份记忆在流式与整段路径下指纹必须相同")
	}

	// 没有记忆时不应误报有记忆块。
	noMem := &DirectAgent{LLM: &scriptedLLM{}, Assembler: testAssembler("S")}
	out4, err := noMem.Run(ctx, Input{Query: "hi", SessionKey: key})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out4.MemoryDigest != "" {
		t.Fatalf("无记忆时 MemoryDigest 应为空: %q", out4.MemoryDigest)
	}
}

// Test_F48_MemoryInstructionRequiresSpeakerAttribution 钉住记忆指令里的**归属要求**。
//
// 背景：记忆按会话共享（群聊的 session key 里 UserID 为 0），若存下来的事实不带主语，
// 「张三很怕辣」会退化成「很怕辣」，李四问起来就会答错人。
// 因此指令必须同时要求两件事：写清属于谁、以及只转述当前发言人的记忆。
func Test_F48_MemoryInstructionRequiresSpeakerAttribution(t *testing.T) {
	t.Parallel()
	instr := ProactiveMemoryInstruction("")
	for _, want := range []string{"属于谁", "昵称", "当前发言人"} {
		if !strings.Contains(instr, want) {
			t.Fatalf("记忆指令应包含归属要求 %q，实际: %s", want, instr)
		}
	}
	// 自定义指令仍然优先（运维可以覆盖）；这里只确认默认值本身的契约。
	if got := ProactiveMemoryInstruction("自定义"); got != "自定义" {
		t.Fatalf("自定义指令应优先生效: %q", got)
	}
}
