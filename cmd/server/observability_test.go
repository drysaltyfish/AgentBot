package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/ops"
	"github.com/drysaltyfish/agentbot/internal/store"
)

func findCheck(checks []ops.Check, name string) (ops.Check, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return ops.Check{}, false
}

// Test_F69_ReadinessChecksReflectDependencies 钉住就绪检查的判据：
// 存储可查、传输已连、provider 已配置；任一不满足都要能被 /readyz 报出来。
func Test_F69_ReadinessChecksReflectDependencies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "ready.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	var up atomic.Bool
	ready := readinessChecks(st, &up, "")

	checks := ready(ctx)
	if c, _ := findCheck(checks, "storage"); !c.OK {
		t.Fatalf("存储应可用: %+v", c)
	}
	if c, _ := findCheck(checks, "transport"); c.OK {
		t.Fatalf("未连接时 transport 不应就绪: %+v", c)
	}
	if c, _ := findCheck(checks, "llm"); c.OK {
		t.Fatalf("provider 未配置时 llm 不应就绪: %+v", c)
	}

	up.Store(true)
	ready = readinessChecks(st, &up, "deepseek")
	for _, c := range ready(ctx) {
		if !c.OK {
			t.Fatalf("全部依赖就绪时不该有失败项: %+v", c)
		}
	}

	// 存储关掉后必须能被探针发现，而不是继续报就绪。
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c, _ := findCheck(ready(ctx), "storage"); c.OK {
		t.Fatalf("存储不可用时 storage 不应就绪")
	}
}

// Test_F68_RouteObserverFeedsCatalog 钉住路由观测口到指标的映射（F-68）。
func Test_F68_RouteObserverFeedsCatalog(t *testing.T) {
	t.Parallel()
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	rm := routeMetrics{cat: cat}
	rm.RouteMatched("reply", 10*time.Millisecond)
	rm.RouteMatched("reply", 20*time.Millisecond)
	rm.RoutePanicked("reply")
	rm.RouteMatched("", 0) // 未命名路由归一到 unnamed

	var b strings.Builder
	if err := cat.Registry.WritePrometheus(&b); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"route_matches_total{route=\"reply\"} 2",
		"route_matches_total{route=\"unnamed\"} 1",
		"route_errors_total{route=\"reply\"} 1",
		"handler_duration_seconds_count{route=\"reply\"} 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("指标输出缺少 %q\n%s", want, out)
		}
	}
}
