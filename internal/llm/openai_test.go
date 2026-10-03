package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sseBody = `data: {"choices":[{"delta":{"content":"hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func newOpenAITest(t *testing.T, handler http.HandlerFunc) (*OpenAI, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOpenAI(OpenAIConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "test-model",
		Client:  srv.Client(),
	}), srv
}

func Test_F26_WireMappingPreservesToolProtocol(t *testing.T) {
	t.Parallel()
	var captured []byte
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		captured = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	})

	req := &ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a bot"},
			{Role: RoleUser, Content: "weather?"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "weather", Arguments: `{"city":"SH"}`}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "sunny"},
		},
		Tools: []ToolSpec{{Name: "weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	if _, err := client.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var wire struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(captured, &wire); err != nil {
		t.Fatalf("decode captured request: %v (body=%s)", err, captured)
	}
	if wire.Model != "test-model" {
		t.Fatalf("model: actual=%q expected=test-model", wire.Model)
	}
	if wire.Stream {
		t.Fatalf("non-stream Chat must set stream=false")
	}
	if len(wire.Messages) != 4 {
		t.Fatalf("messages: actual=%d expected=4", len(wire.Messages))
	}
	assistant := wire.Messages[2]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant message lost tool_calls: %+v", assistant)
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Type != "function" {
		t.Fatalf("tool_call id/type: %+v", assistant.ToolCalls[0])
	}
	if assistant.ToolCalls[0].Function.Name != "weather" || assistant.ToolCalls[0].Function.Arguments != `{"city":"SH"}` {
		t.Fatalf("tool_call function payload: %+v", assistant.ToolCalls[0].Function)
	}
	tool := wire.Messages[3]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" {
		t.Fatalf("tool message lost tool_call_id: %+v", tool)
	}
	if len(wire.Tools) != 1 || wire.Tools[0].Type != "function" || wire.Tools[0].Function.Name != "weather" {
		t.Fatalf("tools mapping: %+v", wire.Tools)
	}
}

func Test_F26_ChatParsesResponseAndUsage(t *testing.T) {
	t.Parallel()
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization header: actual=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there","tool_calls":[{"id":"c9","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})
	resp, err := client.Chat(context.Background(), userReq())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hi there" || resp.FinishReason != "tool_calls" {
		t.Fatalf("response: %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c9" || resp.ToolCalls[0].Name != "f" {
		t.Fatalf("tool calls: %+v", resp.ToolCalls)
	}
	if resp.Usage.TotalTokens != 15 || resp.Usage.PromptTokens != 10 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func Test_F26_UpstreamStatusBecomesStatusError(t *testing.T) {
	t.Parallel()
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	})
	_, err := client.Chat(context.Background(), userReq())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("Chat: actual=%v expected=*StatusError", err)
	}
	if se.StatusCode != 401 {
		t.Fatalf("status: actual=%d expected=401", se.StatusCode)
	}
	if DefaultRetryable(err) {
		t.Fatalf("401 must not be retryable")
	}
}

func Test_F26_EmptyMessagesRejectedBeforeHittingNetwork(t *testing.T) {
	t.Parallel()
	hit := false
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
	})
	if _, err := client.Chat(context.Background(), &ChatRequest{}); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("Chat: actual=%v expected=ErrNoMessages", err)
	}
	if _, err := client.ChatStream(context.Background(), &ChatRequest{}); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("ChatStream: actual=%v expected=ErrNoMessages", err)
	}
	if hit {
		t.Fatalf("empty request must be rejected before the network call")
	}
}

func Test_F28_ChatStreamParsesSSE(t *testing.T) {
	t.Parallel()
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	})
	ch, err := client.ChatStream(context.Background(), userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var content string
	var done bool
	var finish string
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected chunk error: %v", c.Err)
		}
		content += c.Content
		if c.Done {
			done = true
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}
	if content != "hello" {
		t.Fatalf("streamed content: actual=%q expected=hello", content)
	}
	if !done {
		t.Fatalf("stream did not emit a terminal chunk")
	}
	if finish != "stop" {
		t.Fatalf("finish reason: actual=%q expected=stop", finish)
	}
}

func Test_F28_ChatStreamStatusErrorIsReturnedSynchronously(t *testing.T) {
	t.Parallel()
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	})
	_, err := client.ChatStream(context.Background(), userReq())
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 502 {
		t.Fatalf("ChatStream: actual=%v expected=*StatusError(502)", err)
	}
	if !DefaultRetryable(err) {
		t.Fatalf("502 must be retryable")
	}
}

func Test_F26_EndpointIsBuiltFromBaseURL(t *testing.T) {
	t.Parallel()
	var path string
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`))
	})
	if _, err := client.Chat(context.Background(), userReq()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.HasSuffix(path, "/chat/completions") {
		t.Fatalf("endpoint path: actual=%q expected suffix /chat/completions", path)
	}
}

// newOpenAITestWith 与 newOpenAITest 相同，但允许自定义配置（思考档位等）。
func newOpenAITestWith(t *testing.T, cfg OpenAIConfig, handler http.HandlerFunc) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.Client = srv.Client()
	return NewOpenAI(cfg)
}

const cacheUsageJSON = `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2,"completion_tokens_details":{"reasoning_tokens":5}}`

// Test_F26_ThinkingParamsAreSent 覆盖 DeepSeek 思考模式的线上形态与缓存计量解析。
func Test_F26_ThinkingParamsAreSent(t *testing.T) {
	t.Parallel()
	thinking := true
	var captured map[string]any
	client := newOpenAITestWith(t, OpenAIConfig{Thinking: &thinking, ReasoningEffort: "low"},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&captured)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + cacheUsageJSON + `}`))
		})

	resp, err := client.Chat(context.Background(), &ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Temperature: 0.7,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	th, _ := captured["thinking"].(map[string]any)
	if th == nil || th["type"] != "enabled" {
		t.Fatalf("thinking not sent correctly: %+v", captured["thinking"])
	}
	if captured["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort: actual=%v", captured["reasoning_effort"])
	}
	// 思考模式不支持 temperature：传了不生效，所以不应下发。
	if _, ok := captured["temperature"]; ok {
		t.Fatalf("temperature must not be sent in thinking mode: %v", captured["temperature"])
	}
	if resp.Usage.PromptCacheHitTokens != 8 || resp.Usage.PromptCacheMissTokens != 2 {
		t.Fatalf("cache usage: hit=%d miss=%d", resp.Usage.PromptCacheHitTokens, resp.Usage.PromptCacheMissTokens)
	}
	if resp.Usage.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens: actual=%d", resp.Usage.ReasoningTokens)
	}
	if ratio := resp.Usage.CacheHitRatio(); ratio != 0.8 {
		t.Fatalf("cache hit ratio: actual=%v expected=0.8", ratio)
	}
}

// Test_F26_TemperatureSentWhenThinkingDisabled 保证非思考模式仍然下发 temperature。
func Test_F26_TemperatureSentWhenThinkingDisabled(t *testing.T) {
	t.Parallel()
	thinking := false
	var captured map[string]any
	client := newOpenAITestWith(t, OpenAIConfig{Thinking: &thinking},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&captured)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
		})
	if _, err := client.Chat(context.Background(), &ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Temperature: 0.7,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if captured["temperature"] != 0.7 {
		t.Fatalf("temperature should be sent when thinking is disabled: %v", captured["temperature"])
	}
	th, _ := captured["thinking"].(map[string]any)
	if th == nil || th["type"] != "disabled" {
		t.Fatalf("thinking type: %+v", captured["thinking"])
	}
}

// Test_F26_UnsetThinkingOmitsField 保证未配置时不发 thinking/effort。
//
// 其它 OpenAI 兼容端点（本地 Ollama、vLLM 等）不认这些字段，无条件下发会直接 400。
func Test_F26_UnsetThinkingOmitsField(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	client := newOpenAITestWith(t, OpenAIConfig{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	})
	if _, err := client.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for _, k := range []string{"thinking", "reasoning_effort", "stream_options"} {
		if _, ok := captured[k]; ok {
			t.Fatalf("unset option must not be sent: %s=%v", k, captured[k])
		}
	}
}

// Test_F28_StreamCarriesCacheUsage 保证流式路径也能拿到缓存计量。
func Test_F28_StreamCarriesCacheUsage(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	client := newOpenAITestWith(t, OpenAIConfig{Thinking: boolPtr(true), ReasoningEffort: "low", IncludeStreamUsage: true},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&captured)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"想一想"}}]}

data: {"choices":[{"delta":{"content":"hi"}}]}

data: {"choices":[],"usage":` + cacheUsageJSON + `}

data: [DONE]

`))
		})

	ch, err := client.ChatStream(context.Background(), &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var reasoning, content string
	var usage *Usage
	var done bool
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		reasoning += c.Reasoning
		content += c.Content
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.Done {
			done = true
		}
	}
	if !done {
		t.Fatalf("stream did not terminate")
	}
	if reasoning != "想一想" || content != "hi" {
		t.Fatalf("streamed content: reasoning=%q content=%q", reasoning, content)
	}
	if usage == nil || usage.PromptCacheHitTokens != 9-1 {
		t.Fatalf("terminal chunk must carry cache usage: %+v", usage)
	}
	if captured["stream_options"] == nil {
		t.Fatalf("include_usage must be requested when configured")
	}
}

// Test_F26_CacheHitRatioWithoutData 覆盖无计量时的取值。
func Test_F26_CacheHitRatioWithoutData(t *testing.T) {
	t.Parallel()
	if got := (Usage{}).CacheHitRatio(); got != 0 {
		t.Fatalf("ratio without data: actual=%v", got)
	}
	if got := (Usage{PromptCacheHitTokens: 3, PromptCacheMissTokens: 1}).CacheHitRatio(); got != 0.75 {
		t.Fatalf("ratio: actual=%v", got)
	}
}

func boolPtr(b bool) *bool { return &b }
