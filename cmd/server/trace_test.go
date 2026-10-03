package main

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/trace"
)

// Test_F72_EventTraceContextCarriesW3CIdentity 钉住一处来源：
// 日志的 trace_id 必须就是出站 traceparent 里的 trace id，否则两边对不上，
// "顺着一条链路查"就退化成"看两串无关的随机字符"。
func Test_F72_EventTraceContextCarriesW3CIdentity(t *testing.T) {
	t.Parallel()
	ctx := eventTraceContext(context.Background(), &event.Event{})
	sc, ok := trace.SpanContextFromContext(ctx)
	if !ok || !sc.IsValid() {
		t.Fatalf("事件上下文里没有合法的 span context: %+v", sc)
	}
	if got := observe.TraceID(ctx); got != sc.TraceID.String() {
		t.Fatalf("日志 trace_id 与 W3C trace id 不一致: got=%q want=%q", got, sc.TraceID.String())
	}
	if len(sc.TraceID.String()) != 32 {
		t.Fatalf("trace id 应为 32 位十六进制: %q", sc.TraceID.String())
	}
}

// Test_F72_EventTraceContextIsUnique 保证不同事件不共用同一个 trace id。
func Test_F72_EventTraceContextIsUnique(t *testing.T) {
	t.Parallel()
	a := observe.TraceID(eventTraceContext(context.Background(), &event.Event{}))
	b := observe.TraceID(eventTraceContext(context.Background(), &event.Event{}))
	if a == "" || a == b {
		t.Fatalf("两次事件的 trace id 应各自独立: %q vs %q", a, b)
	}
}
