package testutil_test

// 本文件把 F-75 点名的契约测试集中登记在一处；每个契约都能独立定位到实现。
//
// 五类契约与归属：
//  1. 多轮工具调用契约 —— internal/agent Test_F35_ToolProtocolContract（既有，线路级）：
//     用 httptest 记录请求体，断言第二轮 assistant 带 tool_calls、tool 消息带匹配 id。
//  2. 流式契约 —— 本文件 Test_F75_StreamingContract /
//     Test_F75_StreamingCancelClosesConnection。
//  3. 一个动作端到端契约 —— 本文件 Test_F75_OneActionEndToEndContract：
//     注入一条群消息 -> 真实 OpenAI 兼容实现（httptest）-> Caller 收到的平台请求。
//  4. 传输契约 —— internal/transport Test_F04_FakeDriverDeliversEventsAndListenReturnsOnCancel（既有）。
//  5. 裁剪契约 —— internal/history Test_F38_WindowKeepsToolCallPairIntact /
//     Test_F38_TrimDropsUnrecoverableOrphan（既有）。
//
// 明细与"故意破坏谁会让哪条红"的对应关系见同目录 CONTRACTS.md。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// Test_F75_StreamingContract 覆盖流式契约：mock SSE 流聚合正确。
//
// 内容分片必须合并；带 id 的 tool_call 分片必须原样透传并出现在终止分片之前。
// 生产者在收到 [DONE] 后发终止分片并关闭 channel，错误走 Chunk.Err 而不是 panic。
func Test_F75_StreamingContract(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"hel"}}]}`)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"lo"}}]}`)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"id":"call_s1","type":"function","function":{"name":"echo","arguments":"{\"v\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	client := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Client: srv.Client(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := client.ChatStream(ctx, &llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var (
		content string
		finish  string
		calls   []llm.ToolCall
		done    bool
	)
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("流式分片携带错误：%v", c.Err)
		}
		content += c.Content
		calls = append(calls, c.ToolCalls...)
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Done {
			done = true
		}
	}
	if content != "hello" {
		t.Fatalf("内容分片未正确聚合：actual=%q expected=hello", content)
	}
	if !done || finish != "tool_calls" {
		t.Fatalf("终止分片不正确：Done=%v FinishReason=%q", done, finish)
	}
	if len(calls) != 1 {
		t.Fatalf("tool_call 分片数量：actual=%d expected=1（%+v）", len(calls), calls)
	}
	if calls[0].ID != "call_s1" || calls[0].Name != "echo" || calls[0].Arguments != "{\"v\":\"x\"}" {
		t.Fatalf("tool_call 聚合结果：actual=%+v expected={call_s1 echo {\"v\":\"x\"}}", calls[0])
	}
}

// Test_F75_StreamingCancelClosesConnection 覆盖流式契约的取消部分：
// ctx 取消后生产者必须退出，并且 HTTP 连接被关闭（服务端 Context 收到取消）。
func Test_F75_StreamingCancelClosesConnection(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			close(closed)
			return
		}
		// 持续心跳：只有服务端不断写，客户端 pumpSSE 才能在下一次发送时观察到
		// ctx 取消并关闭响应体，从而关闭连接。用 ticker 而不是 time.Sleep（F-73）。
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				close(closed)
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"); err != nil {
					close(closed)
					return
				}
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	client := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Client: srv.Client(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := client.ChatStream(ctx, &llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("流在取消之前就已关闭")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到首个流式分片")
	}

	cancel()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后连接没有关闭")
	}
	// 消费剩余分片，确认生产者已经退出、channel 已关闭。
	for range ch {
	}
}

// Test_F75_OneActionEndToEndContract 覆盖"一个动作"的端到端契约。
//
// 从注入一条群消息开始，经 reply.Pipeline -> agent.DirectAgent -> 真实 OpenAI 兼容
// 实现（httptest，无外网）-> outbound.Sender -> Caller；断言 Caller 实际收到的
// 平台 API 请求，以及模型请求里确实带着这条群消息。
func Test_F75_OneActionEndToEndContract(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		llmBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		llmBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"收到啦"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	t.Cleanup(srv.Close)

	model := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Client: srv.Client(),
	})
	assembler := conversation.New(conversation.Options{System: "你是测试助手"})
	caller := &contractCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	logger := newContractLogger(t)
	sessions := session.New(
		session.WithHistory(history.NewMemory(64)),
		session.WithTTL(time.Hour),
		session.WithMax(16),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sessions.Close(ctx)
	})

	pipeline := reply.New(reply.Deps{
		Brain:    &agent.DirectAgent{LLM: model, Assembler: assembler},
		Sender:   sender,
		Sessions: sessions,
		Log:      logger,
		Timeout:  5 * time.Second,
	})
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}
	pipeline.Handle(context.Background(), reply.Job{
		Key: key, GroupID: 2, UserID: 3, Text: "你好", TraceID: "f75-e2e", ShouldReply: true,
	})

	reqs := caller.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("Caller 收到的平台请求数：actual=%d expected=1", len(reqs))
	}
	if reqs[0].Action != "send_group_msg" {
		t.Fatalf("平台动作：actual=%q expected=send_group_msg", reqs[0].Action)
	}
	if got := reqs[0].Params["group_id"]; got != int64(2) {
		t.Fatalf("群号：actual=%v expected=2", got)
	}
	raw, err := json.Marshal(reqs[0].Params["message"])
	if err != nil {
		t.Fatalf("序列化消息段：%v", err)
	}
	if !strings.Contains(string(raw), "收到啦") {
		t.Fatalf("平台请求里没有模型回复：%s", raw)
	}

	mu.Lock()
	body := string(llmBody)
	mu.Unlock()
	if !strings.Contains(body, "你好") {
		t.Fatalf("模型请求里没有群消息：%s", body)
	}
	if !strings.Contains(body, "你是测试助手") {
		t.Fatalf("模型请求里没有不可变前缀：%s", body)
	}
}

// contractCaller 记录发往平台的每一次 API 请求，供端到端契约断言。并发安全。
type contractCaller struct {
	mu   sync.Mutex
	reqs []transport.Request
}

func (c *contractCaller) Call(ctx context.Context, req transport.Request) (transport.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return transport.Response{RetCode: 0, Data: json.RawMessage(`{"message_id":1}`)}, nil
}

func (c *contractCaller) snapshot() []transport.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]transport.Request, len(c.reqs))
	copy(out, c.reqs)
	return out
}

func newContractLogger(t *testing.T) *observe.Logger {
	t.Helper()
	lg := observe.New(observe.Options{Level: "error", Format: "json", QueueSize: 64, Writer: io.Discard})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	})
	return lg
}
