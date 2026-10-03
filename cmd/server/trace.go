package main

import (
	"context"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/trace"
)

// eventSampler 决定入站事件是否采样（F-72）。
//
// 默认 1%：trace 的价值是"出问题时能顺着一条链路查"，而不是全量留痕——
// 全采样会让出站头与日志量随流量线性增长，而绝大多数请求没人会去看。
var eventSampler = trace.NewSampler(trace.DefaultSampleRatio)

// eventTraceContext 为一次入站事件建立 W3C trace 上下文（F-72）。
//
// 它是**一处来源、两处使用**：
//   - 日志的 trace_id 取 W3C trace id，于是日志能与出站头逐字对上；
//   - httpx 在发请求时自动带上 traceparent（见 internal/httpx/trace.go）。
//
// 采样标志只影响"要不要记录"，不影响传播：未采样也要带 traceparent，
// 否则上游永远看不到这条链路存在过。
func eventTraceContext(parent context.Context, ev *event.Event) context.Context {
	id, err := trace.NewTraceID()
	if err != nil {
		return observe.WithTraceID(parent, legacyTraceID(ev))
	}
	spanID, serr := trace.NewSpanID()
	if serr != nil {
		return observe.WithTraceID(parent, id.String())
	}
	var flags byte
	if eventSampler.ShouldSample(id, false) {
		flags = trace.FlagSampled
	}
	sc := trace.SpanContext{TraceID: id, SpanID: spanID, Flags: flags}
	return trace.WithSpanContext(observe.WithTraceID(parent, id.String()), sc)
}
