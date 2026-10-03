package metrics

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func Test_F68_CatalogExposesEveryMetric(t *testing.T) {
	c := NewCatalog(CatalogOptions{})

	want := []string{
		"actions_sent_total",
		"events_dropped_total",
		"events_received_total",
		"guard_blocks_total",
		"handler_duration_seconds",
		"llm_latency_seconds",
		"llm_requests_total",
		"llm_tokens_total",
		"queue_depth",
		"rate_limited_total",
		"route_errors_total",
		"route_matches_total",
		"semcache_hits_total",
		"semcache_misses_total",
		"semcache_saved_tokens_total",
		"sessions_active",
		"tool_calls_total",
		"tool_duration_seconds",
	}
	if got := c.MetricNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MetricNames() = %v, want %v", got, want)
	}

	var b strings.Builder
	if err := c.Registry.WritePrometheus(&b); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	out := b.String()
	for _, name := range c.MetricNames() {
		if !strings.Contains(out, name) {
			t.Errorf("exposition missing metric %q", name)
		}
	}
	for _, typ := range []string{"# TYPE handler_duration_seconds histogram", "# TYPE sessions_active gauge", "# TYPE llm_requests_total counter"} {
		if !strings.Contains(out, typ) {
			t.Errorf("exposition missing %q", typ)
		}
	}
}

func Test_F68_LabelValuesAndHistogramRender(t *testing.T) {
	r := NewRegistry()
	req := r.Counter("http_requests_total", "help", "route", "code")
	req.With(Labels{"route": "/x", "code": "200"}).Add(3)

	h := r.Histogram("lat_seconds", "help", []float64{1, 0.1}, "route")
	hist := h.With(Labels{"route": "/x"})
	hist.Observe(0.05)
	hist.Observe(0.5)
	hist.Observe(2)

	g := r.Gauge("inflight", "help")
	g.Set(7)

	var sb strings.Builder
	if err := r.WritePrometheus(&sb); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	out := sb.String()

	wanted := []string{
		"http_requests_total{code=\"200\",route=\"/x\"} 3",
		"lat_seconds_bucket{route=\"/x\",le=\"0.1\"} 1",
		"lat_seconds_bucket{route=\"/x\",le=\"1\"} 2",
		"lat_seconds_bucket{route=\"/x\",le=\"+Inf\"} 3",
		"lat_seconds_sum{route=\"/x\"} 2.55",
		"lat_seconds_count{route=\"/x\"} 3",
		"inflight 7",
	}
	for _, w := range wanted {
		if !strings.Contains(out, w) {
			t.Errorf("exposition missing %q\n---\n%s", w, out)
		}
	}
}

func Test_F68_GaugeFuncReadsAtScrapeTime(t *testing.T) {
	r := NewRegistry()
	sessions := 1.0
	r.GaugeFunc("sessions_active", "help", func() float64 { return sessions })
	r.GaugeFunc("queue_depth", "help", nil)

	var b1 strings.Builder
	if err := r.WritePrometheus(&b1); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(b1.String(), "sessions_active 1") {
		t.Fatalf("first scrape = %q", b1.String())
	}
	sessions = 9
	var b2 strings.Builder
	if err := r.WritePrometheus(&b2); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(b2.String(), "sessions_active 9") {
		t.Fatalf("second scrape = %q", b2.String())
	}
	if !strings.Contains(b2.String(), "queue_depth 0") {
		t.Fatalf("nil gauge func should be zero: %q", b2.String())
	}
}

func Test_F68_ConcurrentAddIsRaceSafe(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("jobs_total", "help", "state")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bound := c.With(Labels{"state": "done"})
			for j := 0; j < 250; j++ {
				bound.Inc()
			}
		}()
	}
	wg.Wait()

	var b strings.Builder
	if err := r.WritePrometheus(&b); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(b.String(), "jobs_total{state=\"done\"} 8000") {
		t.Fatalf("expected 8000, got:\n%s", b.String())
	}
}
