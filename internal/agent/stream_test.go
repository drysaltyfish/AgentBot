package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// Test_F64_DirectAgentRunStreamAggregatesAndFlushes 覆盖 F-64 的接入点：
// 分片被聚合成完整回答，且每一段都通过 splitter 的 flush 回调交给调用方。
func Test_F64_DirectAgentRunStreamAggregatesAndFlushes(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Chunks: []llm.Chunk{
		{Content: "第一句。"},
		{Content: "第二句。"},
		{Done: true, FinishReason: "stop"},
	}})
	var flushed []string
	spl := llm.NewStreamSplitter(llm.StreamConfig{
		MaxChars:      3,
		FirstMinChars: 1,
		Flush:         func(f llm.StreamFlush) { flushed = append(flushed, f.Delta) },
	})
	a := &DirectAgent{LLM: fake, Assembler: testAssembler("sys")}

	out, err := a.RunStream(context.Background(), Input{Query: "问"}, spl)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if out.Text != "第一句。第二句。" {
		t.Fatalf("聚合文本=%q", out.Text)
	}
	if out.LLMCalls != 1 || fake.Streams() != 1 {
		t.Fatalf("流式调用计数: calls=%d streams=%d", out.LLMCalls, fake.Streams())
	}
	if fake.Calls() != 1 {
		t.Fatalf("流式路径应只消费一次脚本（若同时走了 Chat 会是 2），实际 %d", fake.Calls())
	}
	if got := strings.Join(flushed, ""); got != out.Text {
		t.Fatalf("增量拼接=%q, want %q", got, out.Text)
	}
	if len(flushed) < 2 {
		t.Fatalf("应当多次增量发送，实际 %d 次", len(flushed))
	}
}

// Test_F64_DirectAgentRunStreamSurfacesChunkError 钉住"错误也走 channel"（F-28）：
// 中途报错必须作为 error 返回，而不是表现成"回答提前结束"。
func Test_F64_DirectAgentRunStreamSurfacesChunkError(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Chunks: []llm.Chunk{
		{Content: "半截"},
		{Err: errors.New("provider exploded")},
	}})
	spl := llm.NewStreamSplitter(llm.StreamConfig{MaxChars: 2, FirstMinChars: 1, Flush: func(llm.StreamFlush) {}})
	a := &DirectAgent{LLM: fake, Assembler: testAssembler("sys")}

	out, err := a.RunStream(context.Background(), Input{Query: "问"}, spl)
	if err == nil {
		t.Fatal("分片错误必须返回，不能被吞掉")
	}
	if !strings.Contains(out.Text, "半截") {
		t.Fatalf("已经收到的内容应保留: %q", out.Text)
	}
}

// Test_F64_DirectAgentRunStreamWithoutSplitterFallsBack 说明"没给切分器"不等于失败：
// 退回整段路径，调用方拿到的结果与 Run 同形。
func Test_F64_DirectAgentRunStreamWithoutSplitterFallsBack(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Response: &llm.ChatResponse{Content: "整段回答", FinishReason: "stop"}})
	a := &DirectAgent{LLM: fake, Assembler: testAssembler("sys")}

	out, err := a.RunStream(context.Background(), Input{Query: "问"}, nil)
	if err != nil {
		t.Fatalf("RunStream(nil splitter): %v", err)
	}
	if out.Text != "整段回答" || fake.Calls() != 1 {
		t.Fatalf("应退回整段路径: text=%q calls=%d", out.Text, fake.Calls())
	}
}
