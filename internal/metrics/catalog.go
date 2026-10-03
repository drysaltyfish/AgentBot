// Package metrics 的指标目录：F-68 要求"指标必须在同一处定义"。
package metrics

import "sort"

// CatalogOptions 注入无法由注册表自身知道的运行时取值。
type CatalogOptions struct {
	// SessionsActive 返回当前活跃会话数；为 nil 时恒返回 0。
	SessionsActive func() float64
	// QueueDepth 返回当前队列深度；为 nil 时恒返回 0。
	QueueDepth func() float64
}

// Catalog 是所有核心指标的唯一登记处。字段名与指标一一对应，Lead 只需注入它。
type Catalog struct {
	Registry *Registry

	EventsReceived *Counter
	EventsDropped  *Counter
	RouteMatches   *Counter
	RouteErrors    *Counter
	LLMRequests    *Counter
	LLMTokens      *Counter
	ToolCalls      *Counter
	ActionsSent    *Counter
	GuardBlocks    *Counter
	RateLimited    *Counter

	// F-63：语义缓存的命中/未命中与省下的 token。
	SemcacheHits        *Counter
	SemcacheMisses      *Counter
	SemcacheSavedTokens *Counter

	HandlerDuration *Histogram
	LLMLatency      *Histogram
	ToolDuration    *Histogram

	sessionsActive *GaugeFunc
	queueDepth     *GaugeFunc
	names          []string
}

// defaultBuckets 返回标准 Prometheus 秒级直方图上界（每次新建，避免共享可变状态）。
func defaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// NewCatalog 在给定（或新建）注册表上注册 F-68 的全部指标。
func NewCatalog(opts CatalogOptions) *Catalog {
	r := NewRegistry()

	sessionsActive := opts.SessionsActive
	if sessionsActive == nil {
		sessionsActive = func() float64 { return 0 }
	}
	queueDepth := opts.QueueDepth
	if queueDepth == nil {
		queueDepth = func() float64 { return 0 }
	}

	c := &Catalog{Registry: r}

	c.EventsReceived = r.Counter("events_received_total", "Total number of events received.", "kind")
	c.EventsDropped = r.Counter("events_dropped_total", "Total number of events dropped.", "reason")
	c.RouteMatches = r.Counter("route_matches_total", "Total number of route matches.", "route")
	c.RouteErrors = r.Counter("route_errors_total", "Total number of route errors.", "route")
	c.HandlerDuration = r.Histogram("handler_duration_seconds", "Handler duration in seconds.", defaultBuckets(), "route")
	c.LLMRequests = r.Counter("llm_requests_total", "Total number of LLM requests.", "provider", "model", "status")
	c.LLMTokens = r.Counter("llm_tokens_total", "Total number of LLM tokens.", "provider", "model", "type")
	c.LLMLatency = r.Histogram("llm_latency_seconds", "LLM latency in seconds.", defaultBuckets(), "provider", "model")
	c.ToolCalls = r.Counter("tool_calls_total", "Total number of tool calls.", "tool", "status")
	c.ToolDuration = r.Histogram("tool_duration_seconds", "Tool duration in seconds.", defaultBuckets(), "tool")
	c.ActionsSent = r.Counter("actions_sent_total", "Total number of actions sent.", "action", "status")
	c.sessionsActive = r.GaugeFunc("sessions_active", "Number of active sessions.", sessionsActive)
	c.queueDepth = r.GaugeFunc("queue_depth", "Number of queued items.", queueDepth)
	c.GuardBlocks = r.Counter("guard_blocks_total", "Total number of guard blocks.", "guard", "reason")
	c.RateLimited = r.Counter("rate_limited_total", "Total number of rate limited requests.", "scope")
	c.SemcacheHits = r.Counter("semcache_hits_total", "Total number of semantic cache hits.")
	c.SemcacheMisses = r.Counter("semcache_misses_total", "Total number of semantic cache misses.")
	c.SemcacheSavedTokens = r.Counter("semcache_saved_tokens_total", "Total number of tokens saved by the semantic cache.")

	c.names = []string{
		"events_received_total",
		"events_dropped_total",
		"route_matches_total",
		"route_errors_total",
		"handler_duration_seconds",
		"llm_requests_total",
		"llm_tokens_total",
		"llm_latency_seconds",
		"tool_calls_total",
		"tool_duration_seconds",
		"actions_sent_total",
		"sessions_active",
		"queue_depth",
		"guard_blocks_total",
		"rate_limited_total",
		"semcache_hits_total",
		"semcache_misses_total",
		"semcache_saved_tokens_total",
	}
	sort.Strings(c.names)
	return c
}

// MetricNames 返回目录内全部指标名（已排序），供清单与测试核对完整集合。
func (c *Catalog) MetricNames() []string {
	out := make([]string, len(c.names))
	copy(out, c.names)
	return out
}
