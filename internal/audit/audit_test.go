package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// lineRec 是解析后的审计行。
type lineRec map[string]any

func decodeLines(t *testing.T, raw string) []lineRec {
	t.Helper()
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "\n")
	recs := make([]lineRec, 0, len(parts))
	for i, p := range parts {
		var rec lineRec
		if err := json.Unmarshal([]byte(p), &rec); err != nil {
			t.Fatalf("line %d is not JSON: actual=%v expected=json (line=%q)", i, err, p)
		}
		recs = append(recs, rec)
	}
	return recs
}

// gatedWriter 的每次 Write 都阻塞到 gate 被关闭，用于确定性地制造队列满。
type gatedWriter struct {
	gate chan struct{}
	mu   sync.Mutex
	buf  bytes.Buffer
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func Test_F60_ToolCallAndPolicyDeniedProduceJSONLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lg := New(Options{Writer: &buf, QueueSize: 8, Now: func() time.Time { return fixed }})

	lg.Log(Event{
		Type:       EventToolCall,
		TraceID:    "trace-tool",
		SessionKey: "sess-tool",
		UserID:     42,
		GroupID:    7,
		Action:     "kick_member",
		Result:     ResultOK,
		DurationMS: 12,
		Tokens:     34,
		Params:     map[string]string{"target": "123"},
	})
	lg.Log(Event{
		Type:       EventPolicyDenied,
		TraceID:    "trace-policy",
		SessionKey: "sess-policy",
		UserID:     43,
		GroupID:    8,
		Action:     "ban_member",
		Result:     ResultDenied,
		Params:     map[string]string{"reason": "policy"},
	})
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: actual=%v expected=nil", err)
	}

	recs := decodeLines(t, buf.String())
	if len(recs) != 2 {
		t.Fatalf("line count: actual=%d expected=2 (output=%q)", len(recs), buf.String())
	}

	tool := recs[0]
	if tool["type"] != "tool_call" {
		t.Fatalf("type: actual=%v expected=tool_call", tool["type"])
	}
	if tool["result"] != "ok" {
		t.Fatalf("result: actual=%v expected=ok", tool["result"])
	}
	if tool["action"] != "kick_member" {
		t.Fatalf("action: actual=%v expected=kick_member", tool["action"])
	}
	if tool["trace_id"] != "trace-tool" || tool["session_key"] != "sess-tool" {
		t.Fatalf("ids: actual=%v/%v expected=trace-tool/sess-tool", tool["trace_id"], tool["session_key"])
	}
	if tool["user_id"] != float64(42) || tool["group_id"] != float64(7) {
		t.Fatalf("actor: actual=%v/%v expected=42/7", tool["user_id"], tool["group_id"])
	}
	if tool["duration_ms"] != float64(12) || tool["tokens"] != float64(34) {
		t.Fatalf("metrics: actual=%v/%v expected=12/34", tool["duration_ms"], tool["tokens"])
	}
	if tool["time"] != fixed.Format(time.RFC3339Nano) {
		t.Fatalf("time: actual=%v expected=%v", tool["time"], fixed.Format(time.RFC3339Nano))
	}
	params, ok := tool["params"].(map[string]any)
	if !ok || params["target"] != "123" {
		t.Fatalf("params: actual=%v expected=target:123", tool["params"])
	}

	denied := recs[1]
	if denied["type"] != "policy_denied" {
		t.Fatalf("type: actual=%v expected=policy_denied", denied["type"])
	}
	if denied["result"] != "denied" {
		t.Fatalf("result: actual=%v expected=denied", denied["result"])
	}
	if denied["action"] != "ban_member" {
		t.Fatalf("action: actual=%v expected=ban_member", denied["action"])
	}
}

func Test_F60_QueueFullDoesNotBlockAndCountsDrops(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	w := &gatedWriter{gate: gate}
	lg := New(Options{Writer: w, QueueSize: 1, Warn: func(string) {}})

	// 后台 worker 最多取走 1 条就阻塞在 Write 上，队列容量 1，
	// 因此发送 3 条至少丢弃 1 条；Log 若阻塞，本测试会挂起失败。
	for i := 0; i < 3; i++ {
		lg.Log(Event{Type: EventToolCall, Action: "blocked", Result: ResultOK})
	}
	if got := lg.Dropped(); got < 1 {
		t.Fatalf("Dropped: actual=%d expected>=1", got)
	}

	close(gate)
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close after unblock: actual=%v expected=nil", err)
	}
}

func Test_F60_ContentLimitTruncatesAfterRedaction(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := New(Options{
		Writer:       &buf,
		QueueSize:    8,
		ContentLimit: 5,
		Redactor: func(s string) string {
			return strings.ReplaceAll(s, "secret", "***")
		},
	})
	lg.Log(Event{
		Type: EventLLMCall,
		Params: map[string]string{
			"prompt": "secret-abcdefghij",
			"short":  "hi",
		},
	})
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: actual=%v expected=nil", err)
	}

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("redaction leaked raw content: output=%q", out)
	}
	recs := decodeLines(t, out)
	if len(recs) != 1 {
		t.Fatalf("line count: actual=%d expected=1", len(recs))
	}
	params := recs[0]["params"].(map[string]any)
	// "secret-abcdefghij" -> "***-abcdefghij"（14 runes）-> 前 5 runes + 长度后缀。
	want := "***-a…(len=14)"
	if params["prompt"] != want {
		t.Fatalf("prompt: actual=%v expected=%v", params["prompt"], want)
	}
	if params["short"] != "hi" {
		t.Fatalf("short: actual=%v expected=hi", params["short"])
	}
}

func Test_F60_CloseFlushesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := New(Options{Writer: &buf, QueueSize: 16})
	for i := 0; i < 5; i++ {
		lg.Log(Event{Type: EventActionExec, Action: "act", Result: ResultOK})
	}
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("first Close: actual=%v expected=nil", err)
	}
	if got := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; got != 5 {
		t.Fatalf("flushed line count: actual=%d expected=5 (output=%q)", got, buf.String())
	}
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("second Close: actual=%v expected=nil", err)
	}
	// 关闭后写入不得 panic，只计数丢弃。
	lg.Log(Event{Type: EventConfigChanged, Action: "late"})
	if got := lg.Dropped(); got != 1 {
		t.Fatalf("Dropped after close: actual=%d expected=1", got)
	}
}
