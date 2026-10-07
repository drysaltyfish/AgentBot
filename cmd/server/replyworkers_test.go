package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/reply"
)

// waitGroupReachesZero 等到 WaitGroup 归零；超时即失败。
func waitGroupReachesZero(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s 没有归零：成对的 Done 漏了，优雅关闭会一直等", what)
	}
}

// Test_ReplyWorkerLoopDrainsAndAccountsEveryJob 钉住 worker 的两条成对行为。
//
// 每处理一条任务就必须报告一次在途完成；漏掉会让 Bot 的 inflight 等待
// 永远不归零——表现是"关闭很慢直到超时"，而不是任何显式错误。
func Test_ReplyWorkerLoopDrainsAndAccountsEveryJob(t *testing.T) {
	t.Parallel()
	jobs := make(chan reply.Job, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := &sync.WaitGroup{}
	var mu sync.Mutex
	var handled []string
	handle := func(_ context.Context, j reply.Job) {
		mu.Lock()
		handled = append(handled, j.Text)
		mu.Unlock()
	}

	for i := 0; i < 3; i++ {
		wg.Add(1)
		jobs <- reply.Job{Text: fmt.Sprintf("m%d", i)}
	}

	loopDone := make(chan struct{})
	go func() {
		replyWorkerLoop(ctx, jobs, handle, wg, testLogger(t))
		close(loopDone)
	}()

	waitGroupReachesZero(t, wg, "在途计数")

	cancel()
	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 worker 没有退出")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 3 {
		t.Fatalf("应处理 3 条，实际 %d 条: %v", len(handled), handled)
	}
}

// Test_ReplyWorkerLoopSurvivesPanicAndKeepsDraining 钉住"一轮 panic 不带走流水线"。
//
// 若 panic 逃出循环，worker 会静默退出：队列逐渐打满、消息开始被丢弃，
// 表现成"机器人莫名其妙不回话了"，而日志里只有一条孤独的 panic。
func Test_ReplyWorkerLoopSurvivesPanicAndKeepsDraining(t *testing.T) {
	t.Parallel()
	jobs := make(chan reply.Job, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := &sync.WaitGroup{}
	var survived int32
	handle := func(_ context.Context, j reply.Job) {
		if j.Text == "boom" {
			panic("故意炸一轮")
		}
		atomic.AddInt32(&survived, 1)
	}

	// 先炸一条，再给一条正常的：后者必须仍被处理。
	wg.Add(2)
	jobs <- reply.Job{Text: "boom"}
	jobs <- reply.Job{Text: "ok"}

	loopDone := make(chan struct{})
	go func() {
		replyWorkerLoop(ctx, jobs, handle, wg, testLogger(t))
		close(loopDone)
	}()

	waitGroupReachesZero(t, wg, "在途计数（panic 的那条也必须记账）")

	if got := atomic.LoadInt32(&survived); got != 1 {
		t.Fatalf("panic 之后 worker 必须继续工作，实际只处理了 %d 条", got)
	}

	cancel()
	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 worker 没有退出")
	}
}

// Test_ReplyWorkerLoopReturnsWhenQueueIsClosed 覆盖队列被关闭的防御分支。
//
// 不检查 ok 的话，关闭后的 channel 会一直返回零值任务，
// worker 就在空转里烧 CPU，且永远不记账（在途计数越积越多）。
func Test_ReplyWorkerLoopReturnsWhenQueueIsClosed(t *testing.T) {
	t.Parallel()
	jobs := make(chan reply.Job)
	close(jobs)

	loopDone := make(chan struct{})
	go func() {
		replyWorkerLoop(context.Background(), jobs, func(context.Context, reply.Job) {}, &sync.WaitGroup{}, testLogger(t))
		close(loopDone)
	}()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("队列关闭后 worker 没有退出（会在零值任务上空转）")
	}
}

// fakeSpawner 记录起了几个后台任务、退出了几个，并用可控的 ctx 驱动它们。
type fakeSpawner struct {
	ctx context.Context

	mu       sync.Mutex
	started  int
	finished int
}

func (f *fakeSpawner) Go(_ string, fn func(ctx context.Context)) {
	f.mu.Lock()
	f.started++
	ctx := f.ctx
	f.mu.Unlock()
	go func() {
		fn(ctx)
		f.mu.Lock()
		f.finished++
		f.mu.Unlock()
	}()
}

func (f *fakeSpawner) counts() (started, finished int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.finished
}

// Test_StartReplyWorkersFansOutToTheWholePool 覆盖挂到生命周期上的那一步。
//
// 池子大小决定并发度：写成 1 会让 LLM 调用串行，队列迅速打满；
// 一个都没起则表现为"机器人完全不回话"。两种都不会编译失败。
func Test_StartReplyWorkersFansOutToTheWholePool(t *testing.T) {
	t.Parallel()
	const pool = 3
	jobs := make(chan reply.Job, 64)
	wg := &sync.WaitGroup{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp := &fakeSpawner{ctx: ctx}

	var handled int32
	handle := func(context.Context, reply.Job) { atomic.AddInt32(&handled, 1) }

	startReplyWorkers(sp, pool, jobs, handle, wg, testLogger(t))

	if started, _ := sp.counts(); started != pool {
		t.Fatalf("应起 %d 个 worker，实际 %d 个", pool, started)
	}

	// 投得比池子多，确保每个 worker 都被唤起。
	for i := 0; i < pool*4; i++ {
		wg.Add(1)
		jobs <- reply.Job{Text: fmt.Sprintf("m%d", i)}
	}
	waitGroupReachesZero(t, wg, "在途计数")

	if got := atomic.LoadInt32(&handled); got != pool*4 {
		t.Fatalf("应处理 %d 条，实际 %d 条", pool*4, got)
	}

	// ctx 取消后所有 worker 都必须退出：否则优雅关闭会卡在后台任务上直到超时。
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, finished := sp.counts(); finished == pool {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, finished := sp.counts()
	t.Fatalf("ctx 取消后仍有 worker 没退出：started=%d finished=%d", pool, finished)
}
