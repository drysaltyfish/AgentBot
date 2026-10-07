package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// fakeSession 记录连接/断开/读循环的调用序列。
type fakeSession struct {
	mu sync.Mutex

	connectErrs []error // 第 n 次 Connect 返回 connectErrs[n]，用尽后返回 nil
	listenErrs  []error // 第 n 次 Listen 的返回值，用尽后阻塞到 ctx 结束
	calls       []string
	connected   bool
}

func (f *fakeSession) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeSession) seq() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSession) Connect(context.Context) error {
	f.mu.Lock()
	n := 0
	for _, c := range f.calls {
		if c == "connect" {
			n++
		}
	}
	var err error
	if n < len(f.connectErrs) {
		err = f.connectErrs[n]
	}
	if err == nil {
		f.connected = true
	}
	f.mu.Unlock()
	f.record("connect")
	return err
}

func (f *fakeSession) Disconnect() {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
	f.record("disconnect")
}

func (f *fakeSession) Listen(ctx context.Context, _ transport.Sink) error {
	f.mu.Lock()
	n := 0
	for _, c := range f.calls {
		if c == "listen" {
			n++
		}
	}
	var err error
	if n < len(f.listenErrs) {
		err = f.listenErrs[n]
	}
	f.mu.Unlock()
	f.record("listen")
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

type sessionRecorder struct {
	mu    sync.Mutex
	ups   int
	downs int
}

func (r *sessionRecorder) up()   { r.mu.Lock(); r.ups++; r.mu.Unlock() }
func (r *sessionRecorder) down() { r.mu.Lock(); r.downs++; r.mu.Unlock() }
func (r *sessionRecorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ups, r.downs
}

func sessionLogger(t *testing.T) *observe.Logger {
	t.Helper()
	lg := observe.New(observe.Options{Level: "error", Format: "json", QueueSize: 32, Writer: io.Discard})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	})
	return lg
}

// Test_RunWSSessionStopsAtBotIntakeStop 钉住关闭顺序与读循环的配合。
//
// bot.Shutdown 分三步：停入口 → 等在途 → 取消后台 ctx 并等 goroutine 退出。
// "停入口"这一步就是取消 listenCtx，因此读循环必须**在那时**退出——
// 否则第 3 步会一直等这个 goroutine，直到整体超时，关闭变成"卡住 10 秒然后报失败"。
//
// 这条同时覆盖 runWSSession 里那个容易被删掉的判断：Listen 返回后要检查
// ListenCtx 是否已取消，是则直接 return（而不是尝试重连）。
func Test_RunWSSessionStopsAtBotIntakeStop(t *testing.T) {
	t.Parallel()
	listenCtx, stopListen := context.WithCancel(context.Background())
	defer stopListen()

	fs := &fakeSession{} // Listen 一直阻塞到 ctx 结束
	app := bot.New(
		bot.WithShutdownTimeout(2*time.Second),
		bot.WithIntakeStop(func(context.Context) error { stopListen(); return nil }),
	)
	app.Go("ws-session", func(ctx context.Context) {
		runWSSession(ctx, wsSessionDeps{
			Client:    fs,
			ListenCtx: listenCtx,
			OnUp:      func() {},
			OnDown:    func() {},
			Backoff:   func(int) time.Duration { return time.Hour },
			Sleep:     sleepCtx,
			Log:       sessionLogger(t), //nolint:contextcheck // 清理必须用新 ctx：测试自己的 ctx 那时已取消
		})
	})

	// 等它进入读循环。
	deadline := time.After(3 * time.Second)
	for {
		saw := false
		for _, c := range fs.seq() {
			if c == "listen" {
				saw = true
			}
		}
		if saw {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("没有进入读循环，调用序列=%v", fs.seq())
		case <-time.After(5 * time.Millisecond):
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("读循环应在停入口时退出，Shutdown 不该失败: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Shutdown 不该靠超时收场（耗时 %s），说明读循环没有在停入口时退出", d)
	}
}

// noSleep 让退避不真的等待（同时记录被请求的时长）。
func noSleep(*[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		return nil
	}
}

// Test_RunWSSessionReconnectsAfterFailedConnect 钉住"连不上要一直重试"。
//
// 修复前这个 goroutine 失败一次就 return：进程还活着、探针还报 ready，
// 但机器人永远收不到消息，而且没有任何自愈路径。
func Test_RunWSSessionReconnectsAfterFailedConnect(t *testing.T) {
	t.Parallel()
	fs := &fakeSession{connectErrs: []error{errors.New("dial 失败"), errors.New("dial 失败")}}
	rec := &sessionRecorder{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runWSSession(ctx, wsSessionDeps{
			Client:    fs,
			ListenCtx: ctx,
			OnUp:      rec.up,
			OnDown:    rec.down,
			Backoff:   func(int) time.Duration { return time.Millisecond },
			Sleep:     noSleep(nil),
			Log:       sessionLogger(t), //nolint:contextcheck // 清理必须用新 ctx：测试自己的 ctx 那时已取消
		})
		close(done)
	}()

	// 等到第 3 次 connect 发生（前两次失败，第三次成功并进入 Listen）。
	deadline := time.After(3 * time.Second)
	for {
		n := 0
		for _, c := range fs.seq() {
			if c == "connect" {
				n++
			}
		}
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("连接没有重试，调用序列=%v", fs.seq())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if ups, _ := rec.counts(); ups != 1 {
		t.Fatalf("只应在真正连上后置一次 ready，实际 %d", ups)
	}
}

// Test_RunWSSessionDisconnectsBeforeReconnecting 钉住重连前必须丢掉死连接。
//
// transport.WSClient.Connect 在 c.conn != nil 时直接返回 nil。读循环因读错误返回后
// c.conn 仍然是那条死连接，所以若不先 Disconnect，重连会"成功"并拿着死连接再 Listen
// 一次——表现为重连日志刷屏、但一条消息都收不到。
func Test_RunWSSessionDisconnectsBeforeReconnecting(t *testing.T) {
	t.Parallel()
	fs := &fakeSession{listenErrs: []error{errors.New("read frame: EOF")}}
	rec := &sessionRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runWSSession(ctx, wsSessionDeps{
			Client:    fs,
			ListenCtx: ctx,
			OnUp:      rec.up,
			OnDown:    rec.down,
			Backoff:   func(int) time.Duration { return time.Millisecond },
			Sleep:     noSleep(nil),
			Log:       sessionLogger(t), //nolint:contextcheck // 清理必须用新 ctx：测试自己的 ctx 那时已取消
		})
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		seq := fs.seq()
		if len(seq) >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("读循环结束后没有重连，调用序列=%v", seq)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	seq := fs.seq()
	// 期望的前缀：connect, listen, disconnect, connect
	want := []string{"connect", "listen", "disconnect", "connect"}
	for i, w := range want {
		if i >= len(seq) || seq[i] != w {
			t.Fatalf("重连顺序不对: actual=%v expected 前缀=%v", seq, want)
		}
	}
}

// Test_RunWSSessionMarksNotReadyOnDisconnect 钉住断开必须反映到探针上。
//
// wsUp 过去只被置 true、从不回 false，于是"进程健康但收不到消息"在监控上
// 完全看不出来——这是最难排查的形态。
func Test_RunWSSessionMarksNotReadyOnDisconnect(t *testing.T) {
	t.Parallel()
	fs := &fakeSession{listenErrs: []error{errors.New("read frame: EOF")}}
	rec := &sessionRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runWSSession(ctx, wsSessionDeps{
			Client:    fs,
			ListenCtx: ctx,
			OnUp:      rec.up,
			OnDown:    rec.down,
			Backoff:   func(int) time.Duration { return time.Millisecond },
			Sleep:     noSleep(nil),
			Log:       sessionLogger(t), //nolint:contextcheck // 清理必须用新 ctx：测试自己的 ctx 那时已取消
		})
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		if _, down := rec.counts(); down >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("断开后没有把 ready 置回 false")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// Test_RunWSSessionStopsOnContextCancel 取消 ctx 后循环必须退出（且不泄漏 goroutine）。
func Test_RunWSSessionStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	fs := &fakeSession{connectErrs: []error{errors.New("dial 失败")}}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runWSSession(ctx, wsSessionDeps{
			Client:    fs,
			ListenCtx: ctx,
			OnUp:      func() {},
			OnDown:    func() {},
			Backoff:   func(int) time.Duration { return time.Hour },
			Sleep:     sleepCtx,
			Log:       sessionLogger(t), //nolint:contextcheck // 清理必须用新 ctx：测试自己的 ctx 那时已取消
		})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond) // 让它进入退避等待
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后重连循环没有退出")
	}
}
