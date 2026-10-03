package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/metrics"
)

func catalogServer(t *testing.T, ready Readiness, reg *metrics.Registry, authToken string) *Server {
	t.Helper()
	s := New(Options{Registry: reg, Ready: ready, AuthToken: authToken, CacheTTL: time.Minute})
	s.SetReady(true)
	return s
}

func get(t *testing.T, s *Server, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func Test_F69_HealthzStays200WhenDependencyFails(t *testing.T) {
	ready := func(context.Context) []Check {
		return []Check{{Name: "store", OK: false, Err: "disk full"}}
	}
	s := catalogServer(t, ready, nil, "")
	resp := get(t, s, PathHealthz, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func Test_F69_ReadyzListsFailingCheck(t *testing.T) {
	ready := func(context.Context) []Check {
		return []Check{
			{Name: "llm", OK: true},
			{Name: "store", OK: false, Err: "disk full"},
		}
	}
	s := catalogServer(t, ready, nil, "")
	resp := get(t, s, PathReadyz, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", resp.StatusCode)
	}
	got := body(t, resp)
	if !strings.Contains(got, "store") || !strings.Contains(got, "disk full") {
		t.Fatalf("readyz body = %q, want failing name and error", got)
	}
}

func Test_F69_ReadyzOKWhenAllChecksPass(t *testing.T) {
	ready := func(context.Context) []Check {
		return []Check{{Name: "llm", OK: true}, {Name: "store", OK: true}}
	}
	s := catalogServer(t, ready, nil, "")
	resp := get(t, s, PathReadyz, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", resp.StatusCode)
	}
}

func Test_F69_StartingReturns503WithoutProbing(t *testing.T) {
	var calls atomic.Int64
	ready := func(context.Context) []Check {
		calls.Add(1)
		return nil
	}
	s := New(Options{Ready: ready}) // 未调用 SetReady：仍在启动阶段
	resp := get(t, s, PathReadyz, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("starting readyz status = %d, want 503", resp.StatusCode)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("readiness callback invoked %d times while starting, want 0", n)
	}
}

func Test_F69_ReadinessCachedWithinTTL(t *testing.T) {
	var calls atomic.Int64
	ready := func(context.Context) []Check {
		calls.Add(1)
		return []Check{{Name: "llm", OK: true}}
	}
	s := catalogServer(t, ready, nil, "")
	for i := 0; i < 5; i++ {
		if resp := get(t, s, PathReadyz, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("readyz status = %d, want 200", resp.StatusCode)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("readiness callback invoked %d times, want 1 within CacheTTL", n)
	}
}

func Test_F69_MetricsEndpointContainsCatalogNames(t *testing.T) {
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	s := catalogServer(t, nil, cat.Registry, "")
	resp := get(t, s, PathMetrics, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	got := body(t, resp)
	for _, name := range cat.MetricNames() {
		if !strings.Contains(got, name) {
			t.Errorf("metrics missing %q", name)
		}
	}
}

func Test_F69_AuthTokenRequiredOnAllEndpoints(t *testing.T) {
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	s := catalogServer(t, nil, cat.Registry, "s3cret")

	for _, path := range []string{PathHealthz, PathReadyz, PathMetrics} {
		if resp := get(t, s, path, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(t, s, path, map[string]string{"Authorization": "Bearer wrong"}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with wrong token = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(t, s, path, map[string]string{"Authorization": "Bearer s3cret"}); resp.StatusCode != http.StatusOK {
			t.Errorf("%s with token = %d, want 200", path, resp.StatusCode)
		}
	}
}

func Test_F69_StartServesAndCloseIsIdempotent(t *testing.T) {
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	s := New(Options{Addr: "127.0.0.1:0", Registry: cat.Registry})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.Addr() == "127.0.0.1:0" || s.Addr() == "" {
		t.Fatalf("Addr() = %q, want actual bound address", s.Addr())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + s.Addr() + PathHealthz)
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
