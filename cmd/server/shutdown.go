package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/ops"
)

// shutdownTarget 是"能被优雅关闭"的最小能力（*bot.Bot 满足）。
type shutdownTarget interface {
	Shutdown(ctx context.Context) error
}

// readiness 是"能被置为未就绪"的最小能力（*ops.Server 满足）。
type readiness interface {
	SetReady(ready bool)
}

// opsReadiness 把可能为 nil 的 *ops.Server 转成 readiness。
//
// 为什么需要它：ops 关闭时 buildOps 返回 nil，而把一个 nil 的 *ops.Server
// 直接塞进接口，会得到一个**非 nil 的接口**（里面装着 nil 指针）——
// 于是 awaitShutdown 里的 `d.Ops != nil` 判空失效，对 nil 接收者调用 SetReady
// 直接 panic。传 nil 时这里返回真正的 nil 接口，判空才成立。
func opsReadiness(s *ops.Server) readiness {
	if s == nil {
		return nil
	}
	return s
}

// eventDrainer 是"能排空在途事件"的最小能力（*backpressure.Queue[eventJob] 满足）。
type eventDrainer interface {
	Close(ctx context.Context) error
}

// shutdownDeps 是优雅关闭所需的装配件。
//
// Signals 与 Exit 是可注入的：前者让测试不必真发信号，后者让"第二次信号强制退出"
// 这条路径能被断言——否则它只能靠读代码相信它存在。
type shutdownDeps struct {
	App        shutdownTarget
	Ops        readiness
	EventQueue eventDrainer
	Log        *observe.Logger
	Stderr     io.Writer
	Timeout    time.Duration
	Signals    <-chan os.Signal
	Exit       func(int)
}

// awaitShutdown 阻塞到收到退出信号，随后按预算优雅关闭；返回进程退出码。
//
// 顺序有意义，而且改错了不会有任何编译或测试错误：
//
//  1. 先把 ops 置为未就绪——探针要在关闭一开始就反映"正在关闭"，
//     否则负载均衡还会继续往一个正在拆除的进程上送流量。
//  2. 再停事件入口并排空队列，让在途事件跑完（队列满/超时只告警，不阻断关闭）。
//  3. 最后才 Shutdown 组件。反过来做的话，关闭期间到达的事件会交给一个
//     正在拆除的 Bot 处理。
//
// 第二次信号走强制退出：优雅关闭可能卡住，必须留一条"按两次就立刻走"的路。
func awaitShutdown(d shutdownDeps) int {
	lifecycle := d.Log.Component("lifecycle")

	<-d.Signals
	lifecycle.Info("shutdown signal received", "timeout", d.Timeout.String())

	if d.Ops != nil {
		d.Ops.SetReady(false)
	}
	if d.EventQueue != nil {
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), 3*time.Second)
		if err := d.EventQueue.Close(drainCtx); err != nil {
			lifecycle.Warn("event queue did not drain in time", "error", err)
		}
		cancelDrain()
	}

	// 第二次信号走强制退出：优雅关闭可能卡住，必须留一条"按两次就立刻走"的路。
	//
	// 用一个 stop 通道收尾：优雅关闭正常跑完时这条 goroutine 不该继续挂着等信号
	// （生产里进程马上就退了，但测试与嵌入使用会看得见这个泄漏）。
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-d.Signals:
			_, _ = fmt.Fprintln(d.Stderr, "second signal received: forcing exit")
			d.Exit(1)
		case <-stopWatch:
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), d.Timeout)
	defer cancel()
	if err := d.App.Shutdown(ctx); err != nil {
		lifecycle.Error("shutdown incomplete", "error", err)
		return 1
	}
	lifecycle.Info("shutdown complete")
	return 0
}
