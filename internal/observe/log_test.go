package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func Test_F67_SameTraceIDAcrossComponents(t *testing.T) {
	t.Parallel()
	w := &syncWriter{}
	lg := New(Options{Level: "info", Format: "json", QueueSize: 64, Writer: w})
	ctx := WithTraceID(context.Background(), "trace-abc")

	WithContext(ctx, lg.Component("router")).Info("matched", "route", "ping")
	WithContext(ctx, lg.Component("llm")).Info("request", "model", "m")
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: actual=%v expected=nil", err)
	}

	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count: actual=%d expected=2 (output=%q)", len(lines), w.String())
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not JSON: actual=%v expected=json", i, err)
		}
		if rec[TraceIDKey] != "trace-abc" {
			t.Fatalf("line %d trace_id: actual=%v expected=trace-abc", i, rec[TraceIDKey])
		}
		if rec["component"] == nil {
			t.Fatalf("line %d missing component: actual=%v", i, rec)
		}
	}
}

func Test_F67_DebugContentGatesFullText(t *testing.T) {
	t.Parallel()
	const msg = "用户发送的完整消息内容"
	for _, tc := range []struct {
		debug bool
		want  bool
	}{
		{debug: false, want: false},
		{debug: true, want: true},
	} {
		w := &syncWriter{}
		lg := New(Options{Level: "info", Format: "json", QueueSize: 64, Writer: w, DebugContent: tc.debug})
		lg.Info("event received", "length", len([]rune(msg)), "content", lg.Content(msg))
		if err := lg.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		got := strings.Contains(w.String(), msg)
		if got != tc.want {
			t.Fatalf("debug_content=%v: actual contains full text=%v expected=%v (output=%q)", tc.debug, got, tc.want, w.String())
		}
		if got := lg.DebugContent(); got != tc.debug {
			t.Fatalf("DebugContent(): actual=%v expected=%v", got, tc.debug)
		}
	}
}

func Test_F67_ComponentLevelOverrideLowersRootLevel(t *testing.T) {
	t.Parallel()
	w := &syncWriter{}
	lg := New(Options{
		Level:      "warn",
		Format:     "json",
		QueueSize:  64,
		Writer:     w,
		Components: map[string]string{"llm": "debug"},
	})
	lg.Component("router").Debug("router debug should be filtered")
	lg.Component("llm").Debug("llm debug should pass")
	lg.Component("llm").Warn("llm warn passes")
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	out := w.String()
	if strings.Contains(out, "router debug should be filtered") {
		t.Fatalf("router debug leaked past root level warn; output=%q", out)
	}
	if !strings.Contains(out, "llm debug should pass") {
		t.Fatalf("llm debug was filtered despite component override; output=%q", out)
	}
	if !strings.Contains(out, "llm warn passes") {
		t.Fatalf("llm warn was filtered; output=%q", out)
	}
}

func Test_F67_QueueFullDropsAndCounts(t *testing.T) {
	t.Parallel()
	w := &syncWriter{}
	lg := New(Options{Level: "info", Format: "json", QueueSize: 1, Writer: slowWriter{w: w, d: time.Millisecond}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			lg.Info("burst", "i", i)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("logging blocked: actual=blocked expected=non-blocking")
	}
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if lg.Dropped() == 0 {
		t.Fatalf("drop counter: actual=0 expected>0")
	}
}

type slowWriter struct {
	w io.Writer
	d time.Duration
}

func (s slowWriter) Write(p []byte) (int, error) {
	time.Sleep(s.d)
	return s.w.Write(p)
}

func Test_F67_RedactorIsApplied(t *testing.T) {
	t.Parallel()
	w := &syncWriter{}
	lg := New(Options{
		Level: "info", Format: "json", QueueSize: 64, Writer: w,
		Redactor: func(s string) string { return strings.ReplaceAll(s, "SECRET", "***") },
	})
	lg.Info("auth", "token", "SECRET")
	if err := lg.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if strings.Contains(w.String(), "SECRET") {
		t.Fatalf("redactor was not applied: %q", w.String())
	}
}

func Test_F67_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	lg := New(Options{Level: "info", Format: "text", QueueSize: 8, Writer: &syncWriter{}})
	ctx := context.Background()
	if err := lg.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lg.Close(ctx); err != nil {
		t.Fatalf("second Close: actual=%v expected=nil", err)
	}
	lg.Info("after close")
	if lg.Dropped() == 0 {
		t.Fatalf("writing after close should be counted as dropped: actual=0 expected>0")
	}
}

func Test_F67_RecoverTurnsPanicIntoError(t *testing.T) {
	t.Parallel()
	w := &syncWriter{}
	lg := New(Options{Level: "info", Format: "json", QueueSize: 16, Writer: w})
	var err error
	func() {
		defer func() { Recover(lg.Component("router"), "router", recover(), &err) }()
		panic("kaboom")
	}()
	if err == nil {
		t.Fatalf("Recover: actual=nil expected=error")
	}
	if cerr := lg.Close(context.Background()); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if !strings.Contains(w.String(), "kaboom") || !strings.Contains(w.String(), "stack") {
		t.Fatalf("recovered panic should record message and stack: %q", w.String())
	}
}

var _ = slog.LevelInfo
