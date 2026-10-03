package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openUsageStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t, Options{Path: filepath.Join(t.TempDir(), "usage.db")})
}

func Test_F85_AddUsageAccumulates(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	ctx := context.Background()

	d := UsageDelta{Requests: 1, ToolCalls: 2, InputTokens: 100, OutputTokens: 20,
		CacheHitTokens: 60, CacheMissTokens: 40, ReasoningTokens: 5, CostUSD: 0.001, PricingVersion: "v1"}
	for i := 0; i < 3; i++ {
		if err := s.AddUsage(ctx, "k", d); err != nil {
			t.Fatalf("AddUsage: %v", err)
		}
	}
	u, err := s.SessionUsage(ctx, "k")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if u.Requests != 3 || u.ToolCalls != 6 {
		t.Fatalf("次数累加错误: requests=%d tool_calls=%d", u.Requests, u.ToolCalls)
	}
	if u.InputTokens != 300 || u.OutputTokens != 60 {
		t.Fatalf("token 累加错误: in=%d out=%d", u.InputTokens, u.OutputTokens)
	}
	if u.CacheHitTokens != 180 || u.CacheMissTokens != 120 {
		t.Fatalf("缓存 token 累加错误: hit=%d miss=%d", u.CacheHitTokens, u.CacheMissTokens)
	}
	if u.ReasoningTokens != 15 {
		t.Fatalf("推理 token 累加错误: %d", u.ReasoningTokens)
	}
	if u.PricingVersion != "v1" {
		t.Fatalf("价格版本未记录: %q", u.PricingVersion)
	}
	if got := u.CacheHitRatio(); got < 0.59 || got > 0.61 {
		t.Fatalf("命中率应约 0.6，实际 %.4f", got)
	}
}

// Test_F85_ConcurrentAccumulationLosesNothing 是 F-85 的核心验收。
func Test_F85_ConcurrentAccumulationLosesNothing(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	ctx := context.Background()

	const workers = 16
	const perWorker = 25
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if err := s.AddUsage(ctx, "hot", UsageDelta{
					Requests: 1, InputTokens: 10, CacheHitTokens: 3, CacheMissTokens: 7,
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发累加出错: %v", err)
	}

	u, err := s.SessionUsage(ctx, "hot")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if want := int64(workers * perWorker); u.Requests != want {
		t.Fatalf("并发累加丢了增量: requests=%d want=%d", u.Requests, want)
	}
	if want := int64(workers * perWorker * 10); u.InputTokens != want {
		t.Fatalf("token 累加丢了增量: %d want %d", u.InputTokens, want)
	}
}

func Test_F85_SessionsAreIsolated(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	ctx := context.Background()
	_ = s.AddUsage(ctx, "a", UsageDelta{Requests: 1, InputTokens: 100})
	_ = s.AddUsage(ctx, "b", UsageDelta{Requests: 2, InputTokens: 200})

	ua, _ := s.SessionUsage(ctx, "a")
	if ua.Requests != 1 || ua.InputTokens != 100 {
		t.Fatalf("会话 a 的数据被串了: %+v", ua)
	}
	ub, _ := s.SessionUsage(ctx, "b")
	if ub.Requests != 2 || ub.InputTokens != 200 {
		t.Fatalf("会话 b 的数据被串了: %+v", ub)
	}
	if _, err := s.SessionUsage(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("不存在的会话应返回 ErrSessionNotFound: %v", err)
	}
}

func Test_F85_TotalsAndTopSessions(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	ctx := context.Background()
	_ = s.AddUsage(ctx, "cheap", UsageDelta{Requests: 1, CostUSD: 0.01, CacheHitTokens: 90, CacheMissTokens: 10})
	_ = s.AddUsage(ctx, "costly", UsageDelta{Requests: 1, CostUSD: 1.5, CacheHitTokens: 10, CacheMissTokens: 90})

	tot, err := s.UsageTotals(ctx)
	if err != nil {
		t.Fatalf("UsageTotals: %v", err)
	}
	if tot.Requests != 2 {
		t.Fatalf("总数错误: %d", tot.Requests)
	}
	if tot.EstimatedCostUSD < 1.50 || tot.EstimatedCostUSD > 1.52 {
		t.Fatalf("总成本错误: %f", tot.EstimatedCostUSD)
	}
	if r := tot.CacheHitRatio(); r < 0.49 || r > 0.51 {
		t.Fatalf("全局命中率应约 0.5，实际 %.4f", r)
	}

	top, err := s.TopSessions(ctx, 5)
	if err != nil {
		t.Fatalf("TopSessions: %v", err)
	}
	if len(top) != 2 || top[0].SessionKey != "costly" {
		t.Fatalf("应按成本降序: %+v", top)
	}
}

// Test_F85_MessageCountComesFromTableNotCounter 守住"消息数由表实时统计"。
func Test_F85_MessageCountComesFromTableNotCounter(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	ctx := context.Background()
	_ = s.AddUsage(ctx, "k", UsageDelta{Requests: 1, InputTokens: 5})
	for i := 0; i < 4; i++ {
		addMsg(t, s, "k", "user", "user", "内容")
	}
	u, err := s.SessionUsage(ctx, "k")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if u.MessageCount != 4 {
		t.Fatalf("消息数应由 messages 表统计: %d", u.MessageCount)
	}
	// 删除一条后应立即反映，而不是等某个计数器被更新。
	msgs, _ := s.Messages(ctx, "k", 1)
	if err := s.DeleteMessage(ctx, msgs[0].ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	u2, _ := s.SessionUsage(ctx, "k")
	if u2.MessageCount != 3 {
		t.Fatalf("删除后消息数应实时变化: %d", u2.MessageCount)
	}
}

func Test_F85_CacheHitRatioEdgeCases(t *testing.T) {
	t.Parallel()
	if got := (Usage{CacheHitTokens: 0, CacheMissTokens: 0}).CacheHitRatio(); got != 0 {
		t.Fatalf("无 token 时命中率应为 0: %f", got)
	}
	if got := (Usage{CacheHitTokens: 10, CacheMissTokens: 0}).CacheHitRatio(); got != 1 {
		t.Fatalf("全命中应为 1: %f", got)
	}
	// 分母只用 hit+miss：input_tokens 里含命中部分，拿它当分母会把比率算小。
	u := Usage{InputTokens: 100, CacheHitTokens: 80, CacheMissTokens: 20}
	if got := u.CacheHitRatio(); got != 0.8 {
		t.Fatalf("分母应为 hit+miss: %f", got)
	}
}

func Test_F85_AddUsageRejectsEmptyKey(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	if err := s.AddUsage(context.Background(), "  ", UsageDelta{Requests: 1}); err == nil {
		t.Fatalf("空会话键必须报错")
	}
}

// Test_F85_TotalsOnEmptyDatabase 是新装环境第一次查台账的路径。
//
// 空表上 MIN/MAX/SUM 返回 NULL，未经 COALESCE 会直接报扫描错误。
func Test_F85_TotalsOnEmptyDatabase(t *testing.T) {
	t.Parallel()
	s := openUsageStore(t)
	tot, err := s.UsageTotals(context.Background())
	if err != nil {
		t.Fatalf("空库上查总量不应报错: %v", err)
	}
	if tot.Requests != 0 || tot.EstimatedCostUSD != 0 || tot.CacheHitRatio() != 0 {
		t.Fatalf("空库应返回零值: %+v", tot)
	}
	top, err := s.TopSessions(context.Background(), 5)
	if err != nil {
		t.Fatalf("空库上查排行不应报错: %v", err)
	}
	if len(top) != 0 {
		t.Fatalf("空库排行应为空: %+v", top)
	}
}
