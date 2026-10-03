package httpx

import (
	"context"
	"net/http"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/trace"
)

// testSpan 造一个合法的 span context。
func testSpan(t *testing.T) trace.SpanContext {
	t.Helper()
	id, err := trace.NewTraceID()
	if err != nil {
		t.Fatalf("NewTraceID: %v", err)
	}
	sid, err := trace.NewSpanID()
	if err != nil {
		t.Fatalf("NewSpanID: %v", err)
	}
	return trace.SpanContext{TraceID: id, SpanID: sid, Flags: trace.FlagSampled}
}

// Test_F72_TraceparentIsPropagated 是 F-72 出站传播的验收：
// ctx 里有 span 时，httpx 发出的请求必须带可解析的 traceparent。
func Test_F72_TraceparentIsPropagated(t *testing.T) {
	t.Parallel()
	var got string
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(trace.Header)
		w.WriteHeader(http.StatusOK)
	})
	sc := testSpan(t)
	ctx := trace.WithSpanContext(context.Background(), sc)
	if _, err := cfg.Get(ctx, "http://local.test:"+port+"/x"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == "" {
		t.Fatal("出站请求没有带 traceparent")
	}
	back, err := trace.Parse(got)
	if err != nil {
		t.Fatalf("traceparent 不可解析: %q (%v)", got, err)
	}
	if back.TraceID != sc.TraceID || back.SpanID != sc.SpanID || !back.Sampled() {
		t.Fatalf("traceparent 字段不一致: got=%+v want=%+v", back, sc)
	}
}

// Test_F72_NoSpanContextNoHeader 钉住"没有 trace 时不凭空造一个"：
// 凭空造会让上游看到一个与本地日志对不上的 id。
func Test_F72_NoSpanContextNoHeader(t *testing.T) {
	t.Parallel()
	var got string
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(trace.Header)
		w.WriteHeader(http.StatusOK)
	})
	if _, err := cfg.Get(context.Background(), "http://local.test:"+port+"/x"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "" {
		t.Fatalf("无 span 时不该带 traceparent: %q", got)
	}
}

// Test_F72_ExplicitHeaderIsPreserved 保证自动注入不覆盖调用方的显式传播。
func Test_F72_ExplicitHeaderIsPreserved(t *testing.T) {
	t.Parallel()
	var got string
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(trace.Header)
		w.WriteHeader(http.StatusOK)
	})
	ctx := trace.WithSpanContext(context.Background(), testSpan(t))
	explicit := testSpan(t).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local.test:"+port+"/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(trace.Header, explicit)
	resp, err := NewClient(cfg).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got != explicit {
		t.Fatalf("显式 traceparent 被覆盖: got=%q want=%q", got, explicit)
	}
}
