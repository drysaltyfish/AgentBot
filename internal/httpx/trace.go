package httpx

import (
	"net/http"

	"github.com/drysaltyfish/agentbot/internal/trace"
)

// traceTripper 把 ctx 里的 W3C span context 注入出站请求头（F-72）。
//
// 放在 RoundTripper 而不是每个调用点：出站请求散落在 LLM、图片下载、工具 HTTP 里，
// 逐个加头必然会漏；而"这次请求属于哪条 trace"本来就是上下文信息。
//
// 这是**传播**，不是采集：本仓库不导出 OTLP（见 HANDOFF 的已知偏离），
// 头的作用是让上游服务能把它的日志与我们的 trace_id 对上。
type traceTripper struct {
	next http.RoundTripper
}

// RoundTrip 实现 http.RoundTripper。
func (t traceTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	sc, ok := trace.SpanContextFromContext(req.Context())
	if !ok || sc.String() == "" {
		return next.RoundTrip(req)
	}
	// 已有显式头时不覆盖：调用方可能正在做手工传播（例如把上游的 span 原样透传）。
	if req.Header.Get(trace.Header) != "" {
		return next.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	out.Header.Set(trace.Header, sc.String())
	return next.RoundTrip(out)
}
