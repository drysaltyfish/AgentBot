package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// scriptedLLM 按脚本依次返回响应，并记录收到的每个请求。
type scriptedLLM struct {
	mu       sync.Mutex
	requests []*llm.ChatRequest
	replies  []*llm.ChatResponse
	errs     []error
}

func (s *scriptedLLM) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i >= len(s.replies) {
		return &llm.ChatResponse{Content: "（脚本用尽）", FinishReason: "stop"}, nil
	}
	return s.replies[i], nil
}

func (s *scriptedLLM) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.Chunk, error) {
	return nil, llm.ErrNotImplemented
}

func (s *scriptedLLM) request(i int) *llm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		return nil
	}
	return s.requests[i]
}

func (s *scriptedLLM) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// echoTool 把参数原样回显，便于断言 observation。
type echoTool struct {
	name     string
	safe     bool
	failWith string
	calls    int
	mu       sync.Mutex
}

func (e *echoTool) Name() string        { return e.name }
func (e *echoTool) Description() string { return "回显参数" }
func (e *echoTool) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{"v": {Type: "string"}}, Required: []string{"v"}}
}
func (e *echoTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if e.failWith != "" {
		return tool.Result{}, errors.New(e.failWith)
	}
	return tool.Success("echo:" + string(args)), nil
}
func (e *echoTool) ConcurrencySafe() bool { return e.safe }

func newRegistry(t *testing.T, tools ...tool.Tool) *tool.Registry {
	t.Helper()
	r := tool.New()
	for _, x := range tools {
		if err := r.Register(x); err != nil {
			t.Fatalf("register %s: %v", x.Name(), err)
		}
	}
	return r
}

func toolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: args}
}

// Test_F35_ToolProtocolContract 是 F-35 的关键契约测试（线路级）。
//
// 用真实 OpenAI 兼容实现打到 mock server，断言**第二轮请求体**里：
// assistant 消息带 tool_calls、tool 消息带匹配的 tool_call_id。
// 只映射 role/content 而不带这两个字段，会被服务端 400 拒绝（反模式 #14），
// 所以这条断言必须落在序列化后的 JSON 上，而不是内存结构上。
func Test_F35_ToolProtocolContract(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		bodies [][]byte
		round  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		round++
		current := round
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"我来查一下","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"echo","arguments":"{\"v\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"查好了"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":7,"total_tokens":27,"prompt_cache_hit_tokens":18,"prompt_cache_miss_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	model := llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Client: srv.Client(),
	})
	tools := newRegistry(t, &echoTool{name: "echo"})
	a := &ReactAgent{LLM: model, Tools: tools, MaxIterations: 5, Assembler: testAssembler("")}

	out, err := a.Run(context.Background(), Input{Query: "帮我查"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "查好了" {
		t.Fatalf("final text: %q", out.Text)
	}
	if len(bodies) != 2 {
		t.Fatalf("应当恰好两轮请求，实际 %d", len(bodies))
	}

	// —— 契约断言：第二轮请求体 ——
	var second struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bodies[1], &second); err != nil {
		t.Fatalf("解析第二轮请求体: %v", err)
	}

	var assistantWithCalls, toolMsg *int
	for i, m := range second.Messages {
		idx := i
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			assistantWithCalls = &idx
		case m.Role == "tool":
			toolMsg = &idx
		}
	}
	if assistantWithCalls == nil {
		t.Fatalf("第二轮请求里没有带 tool_calls 的 assistant 消息：%s", bodies[1])
	}
	if toolMsg == nil {
		t.Fatalf("第二轮请求里没有 tool 消息：%s", bodies[1])
	}
	if *toolMsg != *assistantWithCalls+1 {
		t.Fatalf("tool 消息必须紧跟其 assistant 消息：assistant=%d tool=%d", *assistantWithCalls, *toolMsg)
	}
	if got := second.Messages[*assistantWithCalls].ToolCalls[0].ID; got != "call_abc" {
		t.Fatalf("tool_call.id 必须原样回传: %q", got)
	}
	if got := second.Messages[*toolMsg].ToolCallID; got != "call_abc" {
		t.Fatalf("tool_call_id 必须匹配上一条 assistant 的调用: %q", got)
	}
	if !strings.Contains(second.Messages[*toolMsg].Content, "echo:") {
		t.Fatalf("tool 消息应携带工具输出: %q", second.Messages[*toolMsg].Content)
	}
	// 工具 schema 必须随每轮下发，否则模型不知道自己能调什么。
	if len(second.Tools) != 1 || second.Tools[0].Function.Name != "echo" {
		t.Fatalf("第二轮请求应继续携带工具 schema: %+v", second.Tools)
	}

	// usage 必须跨轮累计（含缓存计量）。
	if out.Usage.PromptTokens != 30 || out.Usage.CompletionTokens != 12 {
		t.Fatalf("usage 未累计: %+v", out.Usage)
	}
	if out.Usage.PromptCacheHitTokens != 22 || out.Usage.PromptCacheMissTokens != 8 {
		t.Fatalf("缓存计量未累计: %+v", out.Usage)
	}
}

// Test_F35_MaxIterationsReturnsErrorWithSteps 覆盖达到上限的行为。
func Test_F35_MaxIterationsReturnsErrorWithSteps(t *testing.T) {
	t.Parallel()
	replies := make([]*llm.ChatResponse, 0, 3)
	for i := 0; i < 3; i++ {
		replies = append(replies, &llm.ChatResponse{
			ToolCalls:    []llm.ToolCall{toolCall(fmt.Sprintf("c%d", i), "echo", `{"v":"x"}`)},
			FinishReason: llm.FinishReasonToolCalls,
		})
	}
	fake := &scriptedLLM{replies: replies}
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, &echoTool{name: "echo"}), MaxIterations: 3, Assembler: testAssembler("")}

	out, err := a.Run(context.Background(), Input{Query: "转圈"})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("应返回 ErrMaxIterations: %v", err)
	}
	if out.FinishReason != FinishReasonMaxIterations {
		t.Fatalf("finish reason: %q", out.FinishReason)
	}
	// Steps 必须完整：3 轮 ×（1 thought + 1 action）。
	if len(out.Steps) != 6 {
		t.Fatalf("Steps 应完整记录: actual=%d expected=6", len(out.Steps))
	}
	if fake.count() != 3 {
		t.Fatalf("应恰好调用 3 次 LLM: %d", fake.count())
	}
}

// Test_F35_ToolErrorBecomesObservationAndLoopContinues 覆盖"失败也要回灌、循环继续"。
func Test_F35_ToolErrorBecomesObservationAndLoopContinues(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "boom", `{"v":"x"}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "我换个方式", FinishReason: "stop"},
	}}
	tools := newRegistry(t, &echoTool{name: "boom", failWith: "磁盘炸了"})
	a := &ReactAgent{LLM: fake, Tools: tools, Assembler: testAssembler("")}

	out, err := a.Run(context.Background(), Input{Query: "试试"})
	if err != nil {
		t.Fatalf("工具失败不应中断循环: %v", err)
	}
	if out.Text != "我换个方式" {
		t.Fatalf("应当拿到模型的后续回答: %q", out.Text)
	}
	// 第二轮请求里，tool 消息必须是错误文本（observation），而不是空。
	second := fake.request(1)
	last := second.Messages[len(second.Messages)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Content, "磁盘炸了") {
		t.Fatalf("错误必须作为 observation 回灌: %+v", last)
	}
	// 同时要留下 Error 供观测。
	found := false
	for _, s := range out.Steps {
		if s.Type == StepAction && s.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("失败步骤应记录 Error")
	}
}

// Test_F35_UnknownToolBecomesObservation 覆盖模型调了不存在的工具。
func Test_F35_UnknownToolBecomesObservation(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "nope", `{}`)}, FinishReason: llm.FinishReasonToolCalls},
		{Content: "好吧", FinishReason: "stop"},
	}}
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, &echoTool{name: "echo"}), Assembler: testAssembler("")}
	if _, err := a.Run(context.Background(), Input{Query: "x"}); err != nil {
		t.Fatalf("未知工具不应中断: %v", err)
	}
	last := fake.request(1).Messages
	msg := last[len(last)-1]
	if !strings.Contains(msg.Content, "unknown tool") {
		t.Fatalf("未知工具应回灌明确原因: %q", msg.Content)
	}
}

// Test_F35_ContextCancelReturnsImmediately 覆盖 ctx 取消。
func Test_F35_ContextCancelReturnsImmediately(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)}, FinishReason: llm.FinishReasonToolCalls},
	}}
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, &echoTool{name: "echo"}), MaxIterations: 10, Assembler: testAssembler("")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, Input{Query: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应立即返回 context.Canceled: %v", err)
	}
	if fake.count() != 0 {
		t.Fatalf("取消后不应再调用 LLM: %d", fake.count())
	}
}

// Test_F35_ScavengesActionsFromText 覆盖 ADR-0001 的抢救通道。
//
// DeepSeek 思考模式会把工具调用吐在 reasoning/文本里而 tool_calls 为空，
// 这不是理论风险。抢救命中时必须走同一条执行路径。
func Test_F35_ScavengesActionsFromText(t *testing.T) {
	t.Parallel()
	var warns []string
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{Content: `{"action":"echo","params":{"v":"from-text"}}`, FinishReason: "stop"},
		{Content: "好了", FinishReason: "stop"},
	}}
	tools := newRegistry(t, &echoTool{name: "echo"})
	a := &ReactAgent{LLM: fake, Tools: tools, Warn: func(m string) { warns = append(warns, m) }, Assembler: testAssembler("")}

	out, err := a.Run(context.Background(), Input{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "好了" {
		t.Fatalf("抢救后应继续循环并拿到答案: %q", out.Text)
	}
	if fake.count() != 2 {
		t.Fatalf("抢救命中后应再问一轮: %d", fake.count())
	}
	second := fake.request(1)
	var toolMsg *llm.Message
	for i := range second.Messages {
		if second.Messages[i].Role == llm.RoleTool {
			toolMsg = &second.Messages[i]
		}
	}
	if toolMsg == nil || !strings.Contains(toolMsg.Content, "from-text") {
		t.Fatalf("抢救出的动作必须真实执行并回灌: %+v", toolMsg)
	}
	if len(warns) == 0 {
		t.Fatalf("抢救必须告警，否则会长期掩盖 provider 侧问题")
	}
}

// Test_F35_NativeToolCallsWinOverTextActions 覆盖"原生存在时忽略文本动作并告警"。
func Test_F35_NativeToolCallsWinOverTextActions(t *testing.T) {
	t.Parallel()
	var warns []string
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{
			Content:      `{"action":"echo","params":{"v":"from-text"}}`,
			ToolCalls:    []llm.ToolCall{toolCall("native-1", "echo", `{"v":"from-native"}`)},
			FinishReason: llm.FinishReasonToolCalls,
		},
		{Content: "完成", FinishReason: "stop"},
	}}
	tools := newRegistry(t, &echoTool{name: "echo"})
	a := &ReactAgent{LLM: fake, Tools: tools, Protocol: ProtocolAuto, Warn: func(m string) { warns = append(warns, m) }, Assembler: testAssembler("")}

	out, err := a.Run(context.Background(), Input{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 只应执行原生那一个调用。
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "native-1" {
		t.Fatalf("原生存在时必须只执行原生调用: %+v", out.ToolCalls)
	}
	last := fake.request(1).Messages
	msg := last[len(last)-1]
	if strings.Contains(msg.Content, "from-text") {
		t.Fatalf("文本动作不应被执行: %q", msg.Content)
	}
	if len(warns) == 0 {
		t.Fatalf("忽略文本动作时必须告警")
	}
}

// Test_F35_NativeProtocolDisablesScavenging 覆盖 Protocol=native。
func Test_F35_NativeProtocolDisablesScavenging(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{Content: `{"action":"echo","params":{"v":"x"}}`, FinishReason: "stop"},
	}}
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, &echoTool{name: "echo"}), Protocol: ProtocolNative, Assembler: testAssembler("")}
	out, err := a.Run(context.Background(), Input{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.ToolCalls) != 0 {
		t.Fatalf("native 模式下不应抢救文本动作")
	}
	if fake.count() != 1 {
		t.Fatalf("native 模式下一次就应结束: %d", fake.count())
	}
}

// Test_F35_ReasoningContentIsEchoedBackWithTools 覆盖 DeepSeek 的硬性要求。
//
// 携带 tools 时，历史轮次的 reasoning_content 必须回传并会被拼进上下文。
func Test_F35_ReasoningContentIsEchoedBackWithTools(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{replies: []*llm.ChatResponse{
		{
			ReasoningContent: "我先想想……",
			ToolCalls:        []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)},
			FinishReason:     llm.FinishReasonToolCalls,
		},
		{Content: "好了", FinishReason: "stop"},
	}}
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, &echoTool{name: "echo"}), Assembler: testAssembler("")}
	if _, err := a.Run(context.Background(), Input{Query: "x"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var found bool
	for _, m := range fake.request(1).Messages {
		if m.Role == llm.RoleAssistant && m.ReasoningContent == "我先想想……" && len(m.ToolCalls) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("携带 tools 时 reasoning_content 必须随 assistant 消息回传")
	}
}

// slowSafeTool 用于观察并发度。
type slowSafeTool struct {
	name string
	safe bool
	mu   sync.Mutex
	in   int
	peak int
}

func (s *slowSafeTool) Name() string        { return s.name }
func (s *slowSafeTool) Description() string { return "慢工具" }
func (s *slowSafeTool) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{"v": {Type: "string"}}}
}
func (s *slowSafeTool) ConcurrencySafe() bool { return s.safe }
func (s *slowSafeTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	s.mu.Lock()
	s.in++
	if s.in > s.peak {
		s.peak = s.in
	}
	s.mu.Unlock()

	select {
	case <-time.After(30 * time.Millisecond):
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.in--
	s.mu.Unlock()
	return tool.Success("ok"), nil
}

// Test_F35_ParallelRequiresConcurrencySafe 守住"只要有一个不安全就整体串行"。
func Test_F35_ParallelRequiresConcurrencySafe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		safe     bool
		wantPeak int
	}{
		{"声明可并发 → 并发执行", true, 2},
		{"未声明可并发 → 强制串行", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			probe := &slowSafeTool{name: "slow", safe: tc.safe}
			fake := &scriptedLLM{replies: []*llm.ChatResponse{
				{
					ToolCalls: []llm.ToolCall{
						toolCall("c1", "slow", `{"v":"1"}`),
						toolCall("c2", "slow", `{"v":"2"}`),
					},
					FinishReason: llm.FinishReasonToolCalls,
				},
				{Content: "done", FinishReason: "stop"},
			}}
			a := &ReactAgent{
				Assembler:     testAssembler(""),
				LLM:           fake,
				Tools:         newRegistry(t, probe),
				ParallelTools: true,
			}
			out, err := a.Run(context.Background(), Input{Query: "x"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			probe.mu.Lock()
			peak := probe.peak
			probe.mu.Unlock()
			if peak != tc.wantPeak {
				t.Fatalf("并发峰值: actual=%d expected=%d", peak, tc.wantPeak)
			}
			// 无论串行还是并发，ToolCalls 的顺序必须与模型声明的顺序一致，
			// 否则 observation 与调用的对应关系会错位。
			if len(out.ToolCalls) != 2 || out.ToolCalls[0].ID != "c1" || out.ToolCalls[1].ID != "c2" {
				t.Fatalf("ToolCalls 顺序必须与声明一致: %+v", out.ToolCalls)
			}
		})
	}
}
