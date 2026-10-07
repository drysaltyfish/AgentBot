package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

// captureLLM 记录最后一次请求，用来断言"到达 provider 之前"的字节。
type captureLLM struct{ req *llm.ChatRequest }

func (c *captureLLM) Chat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.req = req
	return &llm.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (c *captureLLM) ChatStream(_ context.Context, req *llm.ChatRequest) (<-chan llm.Chunk, error) {
	c.req = req
	ch := make(chan llm.Chunk, 1)
	ch <- llm.Chunk{Done: true, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

var _ llm.LLM = (*captureLLM)(nil)

// Test_F32_BuildBudgetDisabledByDefault 钉住默认行为不变：
// 没配 max_context 时预算为 nil，请求一个字节都不动。
func Test_F32_BuildBudgetDisabledByDefault(t *testing.T) {
	t.Parallel()
	if bw := buildBudget(config.Default(), testLogger(t)); bw.Budget != nil {
		t.Fatalf("未配置 max_context 时不应启用预算: %+v", bw)
	} else if bw.Counter != nil {
		t.Fatal("预算未启用时不该创建实测计数器（只会白占一张表）")
	}
}

// longHistory 造一段远超预算的请求：一条 system + N 条用户消息。
func longHistory(n int) []llm.Message {
	msgs := make([]llm.Message, 0, n+1)
	// 与 conversation.Assembler 一致：system 消息必须是 Pinned，否则裁剪会把它丢掉。
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: "系统提示词（必须保留）", Pinned: true})
	for i := 0; i < n; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("第 %d 条消息：这是一段用来撑爆上下文预算的文本。", i)})
	}
	return msgs
}

// Test_F32_ObservedLLMTrimsBeforeCallingProvider 是 F-32 的接线验收：
// 超长请求必须在**发出之前**被裁到预算内，且 pinned 的 system 始终保留。
func Test_F32_ObservedLLMTrimsBeforeCallingProvider(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.LLM.Model = "m"
	cfg.LLM.MaxContext = ptr(300)
	cfg.LLM.ReserveOutput = ptr(100)
	cfg.LLM.ReserveTools = ptr(50)

	bw := buildBudget(cfg, testLogger(t))
	if bw.Budget == nil {
		t.Fatal("配置了 max_context 应构造出预算")
	}
	budget := bw.Budget
	next := &captureLLM{}
	obs := &observedLLM{next: next, model: "m", budget: budget, counter: bw.Counter}

	msgs := longHistory(200)
	if _, err := obs.Chat(context.Background(), &llm.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if next.req == nil {
		t.Fatal("provider 未被调用")
	}
	sent := next.req.Messages
	if len(sent) >= len(msgs) {
		t.Fatalf("请求未被裁剪: %d -> %d", len(msgs), len(sent))
	}
	if sent[0].Role != llm.RoleSystem {
		t.Fatalf("system 必须保留在首位: %+v", sent[0])
	}
	count, err := llm.HeuristicCounter{}.Count(context.Background(), "m", sent)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count > budget.InputLimit() {
		t.Fatalf("裁剪后 %d token 仍超过预算 %d", count, budget.InputLimit())
	}
}

// usageLLM 返回固定的 prompt token，用来模拟 provider 的实测 usage。
type usageLLM struct {
	promptTokens int
	lastReq      *llm.ChatRequest
}

func (u *usageLLM) Chat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	u.lastReq = req
	return &llm.ChatResponse{
		Content: "ok", FinishReason: "stop",
		Usage: llm.Usage{PromptTokens: u.promptTokens},
	}, nil
}

func (u *usageLLM) ChatStream(_ context.Context, req *llm.ChatRequest) (<-chan llm.Chunk, error) {
	u.lastReq = req
	ch := make(chan llm.Chunk, 1)
	ch <- llm.Chunk{Done: true, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

var _ llm.LLM = (*usageLLM)(nil)

// Test_F32_ProviderUsageIsFedBackIntoTheBudget 钉住 F-32 的"实测优先"真的接上了。
//
// 规格要求 token 计数的优先级是：**provider 真实 usage > 本地 tokenizer > 启发式估算**。
// 这条要靠两处协作：预算拿计数器算占用，观察者在真实响应回来后把 usage 喂回去。
// 两者的计数器必须是同一个——各建一个的话，"接上了"与"没接"表现完全一样，
// 预算永远停在最低那一档的启发式上，而且没有任何报错。
func Test_F32_ProviderUsageIsFedBackIntoTheBudget(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.LLM.Model = "m"
	cfg.LLM.MaxContext = ptr(100000) // 足够大，保证这批消息不会被裁剪
	cfg.LLM.ReserveOutput = ptr(100)
	cfg.LLM.ReserveTools = ptr(50)

	bw := buildBudget(cfg, testLogger(t))
	if bw.Budget == nil || bw.Counter == nil {
		t.Fatal("启用预算时必须同时拿到实测计数器")
	}

	const measured = 4242
	next := &usageLLM{promptTokens: measured}
	obs := &observedLLM{next: next, model: "m", budget: bw.Budget, counter: bw.Counter}

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "系统", Pinned: true},
		{Role: llm.RoleUser, Content: "你好"},
	}
	if _, err := obs.Chat(context.Background(), &llm.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// 关键断言：同一份序列再问一次计数器，必须直接返回 provider 报的实测值，
	// 而不是启发式估算值。
	got, err := bw.Counter.Count(context.Background(), "m", next.lastReq.Messages)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != measured {
		heur, _ := llm.HeuristicCounter{}.Count(context.Background(), "m", next.lastReq.Messages)
		t.Fatalf("实测值没有被喂回计数器：actual=%d，启发式估算=%d，期望实测=%d", got, heur, measured)
	}
}

// Test_F32_ObserveRecordsTheTrimmedSequence 钉住"记的是真正发出去的那份"。
//
// FitRequest 会**换掉**请求对象，发出的是裁剪后的消息序列。
// 若把实测值记在裁剪前的那份上，缓存键与实际请求对不上，
// 下一次同样前缀仍然走启发式——接线看着完成了，效果为零。
func Test_F32_ObserveRecordsTheTrimmedSequence(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.LLM.Model = "m"
	cfg.LLM.MaxContext = ptr(300)
	cfg.LLM.ReserveOutput = ptr(100)
	cfg.LLM.ReserveTools = ptr(50)

	bw := buildBudget(cfg, testLogger(t))
	next := &usageLLM{promptTokens: 7}
	obs := &observedLLM{next: next, model: "m", budget: bw.Budget, counter: bw.Counter}

	original := longHistory(200)
	if _, err := obs.Chat(context.Background(), &llm.ChatRequest{Messages: original}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if next.lastReq == nil || len(next.lastReq.Messages) >= len(original) {
		t.Fatalf("请求未被裁剪，本测试前提不成立: %+v", next.lastReq)
	}

	// 裁剪后的序列应有实测值。
	trimmed, err := bw.Counter.Count(context.Background(), "m", next.lastReq.Messages)
	if err != nil {
		t.Fatalf("Count(trimmed): %v", err)
	}
	if trimmed != 7 {
		t.Fatalf("裁剪后的序列没有被记录实测值: actual=%d expected=7", trimmed)
	}
	// 裁剪前的原始序列不该有实测值（它从未被发出去）。
	untrimmed, err := bw.Counter.Count(context.Background(), "m", original)
	if err != nil {
		t.Fatalf("Count(original): %v", err)
	}
	if untrimmed == 7 {
		t.Fatal("实测值被记到了裁剪前的序列上——缓存键与真实请求对不上，等于没接线")
	}
}

// Test_F32_StreamingPathIsTrimmedToo 保证两条路径看到同一份序列：
// 只在非流式接预算，会让流式回答在超长上下文下被服务端拒绝。
func Test_F32_StreamingPathIsTrimmedToo(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.LLM.Model = "m"
	cfg.LLM.MaxContext = ptr(300)
	cfg.LLM.ReserveOutput = ptr(100)
	cfg.LLM.ReserveTools = ptr(50)

	next := &captureLLM{}
	obs := &observedLLM{next: next, model: "m", budget: buildBudget(cfg, testLogger(t)).Budget}

	msgs := longHistory(200)
	if _, err := obs.ChatStream(context.Background(), &llm.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if next.req == nil || len(next.req.Messages) >= len(msgs) {
		t.Fatalf("流式请求未被裁剪: %+v", next.req)
	}
}
