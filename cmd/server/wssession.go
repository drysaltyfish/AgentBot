package main

import (
	"context"
	"time"

	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// wsSession 是重连循环所需的最小能力（*transport.WSClient 满足）。
//
// 刻意收窄成三个方法而不是直接吃 *transport.WSClient：这样重连本身可以被测试，
// 而重连的**顺序**（先 Disconnect 再 Connect）正是最容易写错的地方。
type wsSession interface {
	Connect(ctx context.Context) error
	Disconnect()
	Listen(ctx context.Context, sink transport.Sink) error
}

// wsSessionDeps 是连接会话的装配件。
type wsSessionDeps struct {
	Client wsSession
	Sink   transport.Sink
	// ListenCtx 只作用于读循环：关闭时先停读循环，再走整体关闭。
	ListenCtx context.Context
	// OnUp / OnDown 反映连接状态，供 readiness 探针使用。
	OnUp   func()
	OnDown func()
	// OnLogin 在连接建立后调用（例如 get_login_info）。
	OnLogin func(ctx context.Context)
	// Backoff 给出第 attempt 次重连前应等待的时长（attempt 从 1 开始）。
	Backoff func(attempt int) time.Duration
	// Sleep 等待，必须可被 ctx 中断。
	Sleep func(ctx context.Context, d time.Duration) error
	Log   *observe.Logger
}

// runWSSession 维持"连接 → 读循环"，断开后按退避重连，直到 ctx 结束。
//
// 这个循环存在的理由：它一旦 return，就再也没人把连接建起来——进程还活着、
// /readyz 还在报 ready（wsUp 过去只被置 true、从不回 false），而机器人永远收不到
// 消息。一次网络抖动或平台重启就足以进入这个状态，并且没有任何自愈路径。
//
// 三个容易写错的点：
//  1. **断开后必须 Disconnect()**。Connect 在 c.conn != nil 时返回 nil，
//     死连接不会被清掉，于是"重连"实际是拿旧连接再 Listen 一次。
//  2. **断开必须调 OnDown**，否则探针会一直报 ready，"进程健康但没有反应"
//     是运维最难看出来的形态。
//  3. 连上之后要把退避计数清零，否则一次成功之后的每次重连都从最大退避开始。
func runWSSession(ctx context.Context, d wsSessionDeps) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		if err := d.Client.Connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			delay := d.Backoff(attempt)
			d.Log.Component("transport").Error("connect failed; will retry",
				"error", err, "attempt", attempt, "retry_in", delay.String())
			if d.Sleep(ctx, delay) != nil {
				return
			}
			continue
		}

		attempt = 0
		d.OnUp()
		if d.OnLogin != nil {
			d.OnLogin(ctx)
		}

		err := d.Client.Listen(d.ListenCtx, d.Sink)
		// 读循环结束：无论正常还是出错，这条连接都不能再用了。
		d.OnDown()
		d.Client.Disconnect()
		if ctx.Err() != nil || d.ListenCtx.Err() != nil {
			return
		}

		attempt++
		delay := d.Backoff(attempt)
		d.Log.Component("transport").Warn("read loop stopped; will reconnect",
			"error", err, "attempt", attempt, "retry_in", delay.String())
		if d.Sleep(ctx, delay) != nil {
			return
		}
	}
}

// sleepCtx 是可被 ctx 中断的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
