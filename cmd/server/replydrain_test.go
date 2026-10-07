package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/reply"
)

// Test_ShutdownDrainsQueuedRepliesInsteadOfTimingOut 钉住回复链路与关闭顺序的配合。
//
// 关闭顺序是：停入口 → 等 inflight 归零 → 取消后台 ctx。
// 队列里任务的 inflight 计数是在**入队时**加上的，所以它们必须在第 2 步真的跑完。
// 一旦 worker 提前退出、或者入队与 Done 配不上，第 2 步就会一直等到超时才收场——
// 表现为"关闭很慢然后报失败"，而不是任何显式错误。
//
// 之前这条链路只有各自的单元测试（worker 循环、enqueue 回滚），
// "入队的东西会被 worker 取走、并让关闭顺利结束"这个跨组件事实没有被断言过。
func Test_ShutdownDrainsQueuedRepliesInsteadOfTimingOut(t *testing.T) {
	t.Parallel()

	const jobs = 6
	gate := make(chan struct{})
	started := make(chan struct{}, jobs)

	var (
		mu      sync.Mutex
		handled []string
	)
	handle := func(_ context.Context, j reply.Job) {
		started <- struct{}{}
		<-gate // 卡住，保证关闭开始时队列里确实还有在途任务
		mu.Lock()
		handled = append(handled, j.Text)
		mu.Unlock()
	}

	queue := make(chan reply.Job, jobs)
	var inflight sync.WaitGroup

	app := bot.New(
		bot.WithShutdownTimeout(3*time.Second),
		// 与 serve() 一致的"等待在途工作"实现：等的是同一个 inflight。
		bot.WithInflightWait(func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { inflight.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	)
	startReplyWorkers(app, 2, queue, handle, &inflight, testLogger(t))

	for i := 0; i < jobs; i++ {
		inflight.Add(1)
		queue <- reply.Job{Text: string(rune('a' + i))}
	}

	// 等两个 worker 各自卡在第一个任务上，此时队列里还有 4 条在途。
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker 没有开始处理任务")
		}
	}

	shutdownDone := make(chan error, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- app.Shutdown(ctx)
	}()

	// 关闭已经开始；放行任务，让它们在"等 inflight 归零"这一步被跑完。
	close(gate)

	var err error
	select {
	case err = <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown 没有返回：在途计数没有归零")
	}
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("队列里的任务应当被跑完，Shutdown 不该失败: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown 耗时 %s，像是靠超时收场而不是把队列排空", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != jobs {
		t.Fatalf("入队 %d 条，实际处理 %d 条：有任务被静默丢弃", jobs, len(handled))
	}
}
