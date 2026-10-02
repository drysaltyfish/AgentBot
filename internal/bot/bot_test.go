package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type recorder struct {
	name string
	log  *[]string
	mu   *sync.Mutex
	err  error
	wait time.Duration
}

func (r *recorder) Name() string { return r.name }

func (r *recorder) Close(ctx context.Context) error {
	if r.wait > 0 {
		select {
		case <-time.After(r.wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	*r.log = append(*r.log, r.name)
	r.mu.Unlock()
	return r.err
}

func Test_F70_ShutdownOrderIsFixed(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var log []string
	b := New(WithIntakeStop(func(ctx context.Context) error {
		mu.Lock()
		log = append(log, "intake")
		mu.Unlock()
		return nil
	}))
	for _, tc := range []struct {
		ph Phase
		n  string
	}{
		{PhaseStorage, "store"},
		{PhaseTransport, "driver"},
		{PhaseSession, "session"},
		{PhaseBackground, "ticker"},
	} {
		if err := b.Register(tc.ph, &recorder{name: tc.n, log: &log, mu: &mu}); err != nil {
			t.Fatalf("register %s: %v", tc.n, err)
		}
	}

	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: actual=%v expected=nil", err)
	}
	want := []string{"intake", "ticker", "session", "driver", "store"}
	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("shutdown order: actual=%v expected=%v", got, want)
	}
	closed := b.ClosedComponents()
	if strings.Join(closed, ",") != "ticker,session,driver,store" {
		t.Fatalf("ClosedComponents: actual=%v expected=[ticker session driver store]", closed)
	}
}

func Test_F70_ShutdownWaitsForInflightWork(t *testing.T) {
	t.Parallel()
	finished := make(chan struct{})
	b := New()
	b.Go("worker", func(ctx context.Context) {
		time.Sleep(50 * time.Millisecond)
		close(finished)
	})
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: actual=%v expected=nil", err)
	}
	select {
	case <-finished:
	default:
		t.Fatalf("Shutdown returned before in-flight work completed")
	}
}

func Test_F70_ShutdownIsIdempotent(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var log []string
	b := New()
	if err := b.Register(PhaseStorage, &recorder{name: "store", log: &log, mu: &mu}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	first := b.Shutdown(ctx)
	second := b.Shutdown(ctx)
	if first != nil || second != nil {
		t.Fatalf("Shutdown errors: actual=%v/%v expected=nil/nil", first, second)
	}
	mu.Lock()
	n := len(log)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("component closed %d times: actual=%d expected=1", n, 1)
	}
}

func Test_F70_ShutdownTimeoutReportsUnfinished(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var log []string
	unblock := make(chan struct{})
	b := New(WithShutdownTimeout(100*time.Millisecond), WithInflightTimeout(100*time.Millisecond))
	if err := b.Register(PhaseStorage, &recorder{name: "blocked-store", log: &log, mu: &mu, wait: time.Hour}); err != nil {
		t.Fatalf("register: %v", err)
	}
	b.Go("wedged", func(ctx context.Context) { <-unblock })

	err := b.Shutdown(context.Background())
	if err == nil {
		t.Fatalf("Shutdown: actual=nil expected=error")
	}
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("error does not wrap ErrShutdownTimeout: actual=%v", err)
	}
	if !strings.Contains(err.Error(), "blocked-store") && !strings.Contains(err.Error(), "in-flight") {
		t.Fatalf("error should name unfinished components: actual=%v", err)
	}
	close(unblock)
	// 等 wedged 真正退出，goleak 才不会误报。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.ClosedComponents()) >= 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func Test_F70_RegisterAfterRunningIsRejected(t *testing.T) {
	t.Parallel()
	b := New()
	b.MarkRunning()
	err := b.Register(PhaseStorage, &recorder{name: "late", log: new([]string), mu: new(sync.Mutex)})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("register after MarkRunning: actual=%v expected=ErrAlreadyRunning", err)
	}
}

func Test_F70_ComponentPanicInBackgroundDoesNotKillProcess(t *testing.T) {
	t.Parallel()
	b := New()
	b.Go("panicky", func(ctx context.Context) { panic("boom") })
	// 给 panic 一点时间发生；Shutdown 必须仍能正常返回。
	time.Sleep(20 * time.Millisecond)
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after background panic: actual=%v expected=nil", err)
	}
}
