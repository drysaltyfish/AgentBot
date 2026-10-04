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
	if b := buildBudget(config.Default(), testLogger(t)); b != nil {
		t.Fatalf("未配置 max_context 时不应启用预算: %+v", b)
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

	budget := buildBudget(cfg, testLogger(t))
	if budget == nil {
		t.Fatal("配置了 max_context 应构造出预算")
	}
	next := &captureLLM{}
	obs := &observedLLM{next: next, model: "m", budget: budget}

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
	obs := &observedLLM{next: next, model: "m", budget: buildBudget(cfg, testLogger(t))}

	msgs := longHistory(200)
	if _, err := obs.ChatStream(context.Background(), &llm.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if next.req == nil || len(next.req.Messages) >= len(msgs) {
		t.Fatalf("流式请求未被裁剪: %+v", next.req)
	}
}
