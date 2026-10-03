package main

import (
	"context"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// startSessionReclaimer 把会话定期回收接到 Bot 的后台生命周期上（F-23）。
//
// 为什么用 app.Go 而不是 session.StartReclaimer：后者的 goroutine 不在 Bot 的
// WaitGroup 里，Shutdown 就不保证它已退出。F-23 明确要求所有后台 goroutine
// 都注册到 Bot，否则"Shutdown 后无残留 goroutine"这条验收无从谈起。
//
// 只把 Manager.Close 接进 PhaseSession 是不够的：那只在进程退出时释放会话，
// 而 F-23 要解决的是"长期运行的机器人把内存与路由表慢慢泄漏掉"。
//
// every <= 0 时用 session.DefaultReclaimEvery。
func startSessionReclaimer(app *bot.Bot, sessions *session.Manager, lg *observe.Logger, every time.Duration) {
	if app == nil || sessions == nil {
		return
	}
	if every <= 0 {
		every = session.DefaultReclaimEvery
	}
	app.Go("session-reclaimer", func(ctx context.Context) {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n := sessions.Reclaim(ctx)
				if n == 0 {
					continue
				}
				if lg != nil {
					lg.Info("reclaimed idle sessions", "count", n, "remaining", sessions.Len())
				}
			}
		}
	})
}
