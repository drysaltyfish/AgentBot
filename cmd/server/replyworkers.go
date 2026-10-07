package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/reply"
)

// goSpawner 是"启动一个受生命周期管理的后台 goroutine"的最小能力。
//
// *bot.Bot 满足它。抽这个接口不是为了多一层抽象，而是为了让"起了几个 worker"
// 这件事可以被断言——此前它只能靠真启动进程来间接观察。
type goSpawner interface {
	Go(name string, fn func(ctx context.Context))
}

// startReplyWorkers 把固定数量的回复 worker 挂到生命周期上。
//
// 每个 worker 独立消费 jobs：一轮回复跑在一个 goroutine 里，互不阻塞。
// LLM 调用是慢操作，串行处理会让队列迅速打满、消息开始被丢弃。
func startReplyWorkers(sp goSpawner, n int, jobs <-chan reply.Job, handle func(context.Context, reply.Job), inflight *sync.WaitGroup, lg *observe.Logger) {
	for i := 0; i < n; i++ {
		sp.Go("reply-worker", func(ctx context.Context) {
			replyWorkerLoop(ctx, jobs, handle, inflight, lg)
		})
	}
}

// replyWorkerLoop 是单个 worker 的主循环：取任务 → 执行 → 记账。
//
// 两条行为必须成对/必须兜住，且都不会以显式错误暴露自己：
//
//   - inflight.Done 与 enqueue 的 inflight.Add 配对。漏掉它，Bot 的
//     inflight 等待永远不会归零——表现是"关闭很慢直到超时"，而不是任何报错。
//   - 每轮回复**就地 recover**。一轮 panic 不该带走整条流水线：
//     否则 worker 静默退出，队列逐渐打满，最后表现为"机器人不回话了"，
//     而日志里只有一条孤独的 panic。
func replyWorkerLoop(ctx context.Context, jobs <-chan reply.Job, handle func(context.Context, reply.Job), inflight *sync.WaitGroup, lg *observe.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				// 队列被关闭也应正常退出，而不是在零值任务上空转。
				// 当前没有调用方会关闭它（关闭走 ctx），这里只是防御。
				return
			}
			func() {
				defer inflight.Done()
				defer func() {
					if rec := recover(); rec != nil {
						lg.Component("reply").Error("worker panic", "panic", fmt.Sprint(rec))
					}
				}()
				handle(ctx, j)
			}()
		}
	}
}
