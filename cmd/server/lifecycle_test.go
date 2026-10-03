package main

import (
	"context"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// Test_F23_ReclaimerIsOwnedByBotLifecycle 覆盖 F-23 的两条验收：
//
//   - 100 个空闲会话在 TTL 过后被回收（内存与路由表回落）；
//   - 回收 goroutine 属于 Bot 的生命周期：Shutdown 返回即证明它已退出。
//
// 第二条用"关闭后再建的会话不会被自动回收"来证明，而不是只看 Shutdown 的返回值：
// 只看返回值的话，一个根本没接到 Bot 上的 goroutine 也能让测试通过。
func Test_F23_ReclaimerIsOwnedByBotLifecycle(t *testing.T) {
	m := session.New(session.WithTTL(10 * time.Millisecond))
	app := bot.New(
		bot.WithShutdownTimeout(2*time.Second),
		// 与组合根一致：在途等待只等回复任务，不等全体后台 goroutine。
		// 否则第 2 步会等一个"要等第 3 步取消上下文才退出"的回收器，必然超时。
		bot.WithInflightWait(func(context.Context) error { return nil }),
	)
	if err := app.Register(bot.PhaseSession, m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	startSessionReclaimer(app, m, nil, 5*time.Millisecond)
	app.MarkRunning()

	for i := 0; i < 100; i++ {
		m.GetOrCreate(m.KeyFor(1, int64(i+1), int64(i+1)))
	}
	if m.Len() != 100 {
		t.Fatalf("前置条件不成立：Len=%d, want 100", m.Len())
	}

	deadline := time.Now().Add(2 * time.Second)
	for m.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.Len() != 0 {
		t.Fatalf("100 个空闲会话未被回收，Len=%d", m.Len())
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// 回收器必须已随 Shutdown 退出：否则这条会话会被下一次 tick 回收。
	m.GetOrCreate(m.KeyFor(2, 1, 1))
	time.Sleep(50 * time.Millisecond)
	if m.Len() != 1 {
		t.Fatalf("Shutdown 后回收 goroutine 仍在运行：Len=%d", m.Len())
	}
}
