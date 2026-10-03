package router

import (
	"context"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// BenchmarkBind 覆盖 F-77 的"状态绑定"基准：Ctx 的 Set/Get 在热路径上。
func BenchmarkBind(b *testing.B) {
	c := NewCtx(context.Background(), event.NewEvent([]byte(groupMessage)), nil)
	args := []string{"a", "b"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(StateKeyArgs, args)
		c.Get(StateKeyArgs)
	}
}

// Test_F77_RouteMatchBudget 给路由匹配设定明确预算并在测试里校验（F-77 验收要求
// "至少有一个基准设定了明确的性能预算并在 CI 中校验"）。
//
// 预算刻意留出 30 倍余量：CI 机器比本地慢得多，这里要拦的是数量级退化，不是抖动。
func Test_F77_RouteMatchBudget(t *testing.T) {
	const budget = 5 * time.Millisecond
	res := testing.Benchmark(BenchmarkRouteMatch)
	if per := res.NsPerOp(); per > int64(budget) {
		t.Fatalf("路由匹配超预算: %d ns/op > %d ns/op", per, int64(budget))
	}
}
