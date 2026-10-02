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
