package main

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/ops"
)

// shutdownRecorder 记录关闭各步的调用顺序。
type shutdownRecorder struct {
	mu    sync.Mutex
	order []string
}

func (r *shutdownRecorder) record(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, step)
}

func (r *shutdownRecorder) steps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

type fakeApp struct {
	rec *shutdownRecorder
	err error
}

func (f fakeApp) Shutdown(context.Context) error {
	f.rec.record("shutdown")
	return f.err
}

type fakeOps struct{ rec *shutdownRecorder }

func (f fakeOps) SetReady(ready bool) {
	if !ready {
		f.rec.record("ops-not-ready")
		return
	}
	f.rec.record("ops-ready")
}

type fakeDrainer struct {
	rec *shutdownRecorder
	err error
}

func (f fakeDrainer) Close(context.Context) error {
	f.rec.record("drain")
	return f.err
}

// shutdownTestLogger 把日志写到 io.Discard，避免测试输出噪音。
func shutdownTestLogger(t *testing.T) *observe.Logger {
	t.Helper()
	lg := observe.New(observe.Options{Level: "error", Format: "json", QueueSize: 16, Writer: io.Discard})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	})
	return lg
}

// Test_OpsReadinessKeepsNilNil 钉住一处会让关闭路径 panic 的坑。
//
// ops 关闭时 buildOps 返回 nil。把 nil 的 *ops.Server 直接塞进 readiness 接口，
// 得到的是"非 nil 的接口持有 nil 指针"，`d.Ops != nil` 判空失效，
// SetReady 会对 nil 接收者解引用。所以必须显式转换。
func Test_OpsReadinessKeepsNilNil(t *testing.T) {
	t.Parallel()
	if got := opsReadiness(nil); got != nil {
		t.Fatalf("nil 必须转成真正的 nil 接口，实际 %#v", got)
	}
	if got := opsReadiness(&ops.Server{}); got == nil {
		t.Fatal("非 nil 的 Server 不应被丢掉")
	}
}

// Test_AwaitShutdownSkipsOpsWhenItIsNil 关闭时没有 ops 也必须安全走完。
func Test_AwaitShutdownSkipsOpsWhenItIsNil(t *testing.T) {
	t.Parallel()
	rec := &shutdownRecorder{}
	sig := make(chan os.Signal, 2)
	sig <- os.Interrupt

	code := awaitShutdown(shutdownDeps{
		App:        fakeApp{rec: rec},
		Ops:        opsReadiness(nil),
		EventQueue: fakeDrainer{rec: rec},
		Log:        shutdownTestLogger(t),
		Stderr:     io.Discard,
		Timeout:    time.Second,
		Signals:    sig,
		Exit:       func(int) {},
	})
	if code != 0 {
		t.Fatalf("没有 ops 时也应正常关闭，实际 %d", code)
	}
	want := []string{"drain", "shutdown"}
	got := rec.steps()
	if len(got) != len(want) {
		t.Fatalf("关闭步骤: actual=%v expected=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("关闭顺序: actual=%v expected=%v", got, want)
		}
	}
}

// Test_AwaitShutdownOrdersStepsCorrectly 钉住关闭的**顺序**。
//
// 顺序改错不会有任何编译或测试错误，只会表现成：
// 探针还在报 ready（流量继续被送进来）、或事件交给一个正在拆除的 Bot。
func Test_AwaitShutdownOrdersStepsCorrectly(t *testing.T) {
	t.Parallel()
	rec := &shutdownRecorder{}
	sig := make(chan os.Signal, 2)
	sig <- os.Interrupt

	code := awaitShutdown(shutdownDeps{
		App:        fakeApp{rec: rec},
		Ops:        fakeOps{rec: rec},
		EventQueue: fakeDrainer{rec: rec},
		Log:        shutdownTestLogger(t),
		Stderr:     io.Discard,
		Timeout:    time.Second,
		Signals:    sig,
		Exit:       func(int) {},
	})

	if code != 0 {
		t.Fatalf("正常关闭应返回 0，实际 %d", code)
	}
	want := []string{"ops-not-ready", "drain", "shutdown"}
	got := rec.steps()
	if len(got) != len(want) {
		t.Fatalf("关闭步骤: actual=%v expected=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("关闭顺序: actual=%v expected=%v", got, want)
		}
	}
}

// Test_AwaitShutdownReportsFailureWithExitCodeOne 关闭失败必须非零退出。
func Test_AwaitShutdownReportsFailureWithExitCodeOne(t *testing.T) {
	t.Parallel()
	rec := &shutdownRecorder{}
	sig := make(chan os.Signal, 2)
	sig <- os.Interrupt

	code := awaitShutdown(shutdownDeps{
		App:        fakeApp{rec: rec, err: errors.New("boom")},
		Ops:        fakeOps{rec: rec},
		EventQueue: fakeDrainer{rec: rec},
		Log:        shutdownTestLogger(t),
		Stderr:     io.Discard,
		Timeout:    time.Second,
		Signals:    sig,
		Exit:       func(int) {},
	})
	if code != 1 {
		t.Fatalf("关闭失败应返回 1，实际 %d", code)
	}
}

// Test_AwaitShutdownDrainsEvenWhenDrainFails 排空超时只告警，不该阻断关闭。
//
// 队列排不空是"还有事件没跑完"，不是"不能关"——卡在排空上等于关不掉。
func Test_AwaitShutdownDrainsEvenWhenDrainFails(t *testing.T) {
	t.Parallel()
	rec := &shutdownRecorder{}
	sig := make(chan os.Signal, 2)
	sig <- os.Interrupt

	code := awaitShutdown(shutdownDeps{
		App:        fakeApp{rec: rec},
		Ops:        fakeOps{rec: rec},
		EventQueue: fakeDrainer{rec: rec, err: errors.New("did not drain")},
		Log:        shutdownTestLogger(t),
		Stderr:     io.Discard,
		Timeout:    time.Second,
		Signals:    sig,
		Exit:       func(int) {},
	})
	if code != 0 {
		t.Fatalf("排空超时不应导致非零退出，实际 %d", code)
	}
	if got := rec.steps(); len(got) != 3 || got[2] != "shutdown" {
		t.Fatalf("排空失败后仍应继续关闭: %v", got)
	}
}

// Test_AwaitShutdownForcesExitOnSecondSignal 钉住"按两次立刻走"这条路。
//
// 优雅关闭可能卡住（在途任务不退出、依赖不响应），没有这条路就只能 kill -9。
func Test_AwaitShutdownForcesExitOnSecondSignal(t *testing.T) {
	t.Parallel()
	rec := &shutdownRecorder{}
	exited := make(chan int, 1)

	sig := make(chan os.Signal, 2)
	sig <- os.Interrupt
	sig <- os.Interrupt // 第二次信号在关闭开始前就已就绪

	// Shutdown 稍微慢一点，给 second-signal 的 goroutine 机会执行。
	app := slowApp{rec: rec, wait: 200 * time.Millisecond}

	code := awaitShutdown(shutdownDeps{
		App:        app,
		Ops:        fakeOps{rec: rec},
		EventQueue: fakeDrainer{rec: rec},
		Log:        shutdownTestLogger(t),
		Stderr:     io.Discard,
		Timeout:    time.Second,
		Signals:    sig,
		Exit:       func(c int) { exited <- c },
	})
	if code != 0 {
		t.Fatalf("主路径仍应正常返回 0，实际 %d", code)
	}

	select {
	case c := <-exited:
		if c != 1 {
			t.Fatalf("强制退出码应为 1，实际 %d", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("第二次信号没有触发强制退出")
	}
}

// slowApp 让 Shutdown 慢一点返回，好观察 second-signal 路径。
type slowApp struct {
	rec  *shutdownRecorder
	wait time.Duration
}

func (s slowApp) Shutdown(context.Context) error {
	s.rec.record("shutdown")
	time.Sleep(s.wait)
	return nil
}
