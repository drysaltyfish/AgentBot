package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func Test_F29_AggregatorConcatenatesFragmentedArguments(t *testing.T) {
	t.Parallel()
	full := `{"city":"Shanghai","days":3,"tags":["a","b"]}`
	// 切成 5 片，保证切点落在引号与转义序列附近。
	parts := []string{`{"ci`, `ty":"Shan`, `ghai","da`, `ys":3,"tags":[`, `"a","b"]}`}
	if len(parts) != 5 {
		t.Fatalf("test fixture must have 5 fragments, got %d", len(parts))
	}

	agg := NewToolCallAggregator()
	agg.Add(ToolCallDelta{Index: 0, ID: "call_1", Name: "get_weather", Arguments: parts[0]})
	for _, p := range parts[1:] {
		agg.Add(ToolCallDelta{Index: 0, Arguments: p})
	}
	got := agg.Finalize()
	if len(got) != 1 {
		t.Fatalf("finalize: actual=%d expected=1", len(got))
	}
	if got[0].Arguments != full {
		t.Fatalf("arguments: actual=%q expected=%q", got[0].Arguments, full)
	}
	if got[0].ID != "call_1" || got[0].Name != "get_weather" {
		t.Fatalf("id/name: %+v", got[0])
	}
	if !json.Valid([]byte(got[0].Arguments)) {
		t.Fatalf("aggregated arguments are not valid JSON: %q", got[0].Arguments)
	}
}

func Test_F29_AggregatorOrdersByIndex(t *testing.T) {
	t.Parallel()
	agg := NewToolCallAggregator()
	// 交错到达：2, 0, 1；参数再交错补全。
	agg.Add(ToolCallDelta{Index: 2, ID: "id2", Name: "c", Arguments: `{"c":`})
	agg.Add(ToolCallDelta{Index: 0, ID: "id0", Name: "a", Arguments: `{"a":`})
	agg.Add(ToolCallDelta{Index: 1, ID: "id1", Name: "b", Arguments: `{"b":`})
	agg.Add(ToolCallDelta{Index: 2, Arguments: "1}"})
	agg.Add(ToolCallDelta{Index: 0, Arguments: "1}"})
	agg.Add(ToolCallDelta{Index: 1, Arguments: "1}"})

	got := agg.Finalize()
	if len(got) != 3 {
		t.Fatalf("finalize: actual=%d expected=3", len(got))
	}
	wantNames := []string{"a", "b", "c"}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Fatalf("order at %d: actual=%q expected=%q (%+v)", i, got[i].Name, want, got)
		}
	}
	if got[0].Arguments != `{"a":1}` || got[2].Arguments != `{"c":1}` {
		t.Fatalf("arguments: %+v", got)
	}
}

func Test_F29_DuplicateNameKeepsFirstAndWarns(t *testing.T) {
	t.Parallel()
	var warnings []string
	agg := NewToolCallAggregator()
	agg.OnWarn = func(s string) { warnings = append(warnings, s) }
	agg.Add(ToolCallDelta{Index: 0, ID: "id", Name: "first", Arguments: "{}"})
	agg.Add(ToolCallDelta{Index: 0, Name: "second", Arguments: ""})
	got := agg.Finalize()
	if len(got) != 1 || got[0].Name != "first" {
		t.Fatalf("duplicate name must keep the first: %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "duplicate name") {
		t.Fatalf("expected exactly one duplicate-name warning: %v", warnings)
	}
}

func Test_F29_IDIsSetOnlyOnce(t *testing.T) {
	t.Parallel()
	agg := NewToolCallAggregator()
	agg.Add(ToolCallDelta{Index: 0, Name: "f", Arguments: `{"a":`})
	agg.Add(ToolCallDelta{Index: 0, ID: "call_1", Arguments: "1}"})
	agg.Add(ToolCallDelta{Index: 0, ID: "call_late", Arguments: "}"})
	got := agg.Finalize()
	if got[0].ID != "call_1" {
		t.Fatalf("id must be set on first non-empty occurrence only: %+v", got[0])
	}
}

// sseJSON 把任意结构编码成一行 SSE data 事件。
func sseJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal sse event: %v", err)
	}
	return "data: " + string(raw) + "\n\n"
}

func Test_F29_StreamAggregatesFragmentsOnFinalChunk(t *testing.T) {
	t.Parallel()
	var body strings.Builder
	body.WriteString(sseJSON(t, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_a", "function": map[string]any{"name": "get_weather", "arguments": `{"ci`},
		}}},
	}}}))
	body.WriteString(sseJSON(t, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"arguments": `ty":"SH"}`},
		}}},
	}}}))
	body.WriteString(sseJSON(t, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 1, "id": "call_b", "function": map[string]any{"name": "get_time", "arguments": "{}"},
		}}},
	}}}))
	body.WriteString(sseJSON(t, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "tool_calls",
	}}}))
	body.WriteString("data: [DONE]\n\n")

	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body.String()))
	})
	ch, err := client.ChatStream(context.Background(), userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var final Chunk
	var sawDone bool
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			sawDone = true
			final = c
		}
	}
	if !sawDone {
		t.Fatalf("stream did not terminate")
	}
	if final.FinishReason != FinishReasonToolCalls {
		t.Fatalf("finish reason: actual=%q", final.FinishReason)
	}
	if len(final.ToolCalls) != 2 {
		t.Fatalf("aggregated tool calls: actual=%d expected=2 (%+v)", len(final.ToolCalls), final.ToolCalls)
	}
	if final.ToolCalls[0].ID != "call_a" || final.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("call 0: %+v", final.ToolCalls[0])
	}
	if final.ToolCalls[0].Arguments != `{"city":"SH"}` {
		t.Fatalf("call 0 arguments: actual=%q", final.ToolCalls[0].Arguments)
	}
	if final.ToolCalls[1].ID != "call_b" || final.ToolCalls[1].Name != "get_time" {
		t.Fatalf("call 1: %+v", final.ToolCalls[1])
	}
}

func Test_F29_StreamFinalizesOnEOF(t *testing.T) {
	t.Parallel()
	var body strings.Builder
	body.WriteString(sseJSON(t, map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_a", "function": map[string]any{"name": "f", "arguments": `{"a":`},
		}}},
	}}}))
	// 没有 [DONE]：scanner 读到 EOF，且不报错，属正常结束——仍应给出聚合结果。
	client, _ := newOpenAITest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body.String()))
	})
	ch, err := client.ChatStream(context.Background(), userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var final Chunk
	for c := range ch {
		if c.Done {
			final = c
		}
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0].Arguments != `{"a":` {
		t.Fatalf("EOF must finalize with what was aggregated: %+v", final.ToolCalls)
	}
}

func Benchmark_F29_AggregatorAddFinalize(b *testing.B) {
	parts := []ToolCallDelta{
		{Index: 0, ID: "c0", Name: "a", Arguments: `{"a":`},
		{Index: 1, ID: "c1", Name: "b", Arguments: `{"b":`},
		{Index: 0, Arguments: "1}"},
		{Index: 1, Arguments: "2}"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		agg := NewToolCallAggregator()
		for _, p := range parts {
			agg.Add(p)
		}
		_ = agg.Finalize()
	}
}
