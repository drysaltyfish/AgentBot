package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/retry"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func Test_F04_FakeDriverDeliversEventsAndListenReturnsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := NewFakeDriver(nil)
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: actual=%v expected=nil", err)
	}
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("second Connect should be idempotent: actual=%v expected=nil", err)
	}

	var got atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- d.Listen(ctx, func(raw []byte, caller Caller) { got.Add(1) })
	}()

	for i := 0; i < 3; i++ {
		if err := d.Push([]byte(`{"post_type":"message"}`)); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	waitFor(t, "3 delivered events", func() bool { return got.Load() == 3 })

	cancel()
	start := time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Listen error: actual=%v expected=context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("Listen did not return within 100ms after cancel")
	}
	_ = start
	if !d.Stopped() {
		t.Fatalf("Stopped(): actual=false expected=true")
	}

	// 返回之后不得再调用 sink。
	before := got.Load()
	_ = d.Push([]byte(`{"post_type":"message"}`))
	time.Sleep(20 * time.Millisecond)
	if after := got.Load(); after != before {
		t.Fatalf("sink called after Listen returned: actual=%d expected=%d", after, before)
	}
}

func Test_F04_ConnectFailureReturnsError(t *testing.T) {
	t.Parallel()
	boom := errors.New("dial failed")
	d := NewFakeDriver(nil)
	d.SetConnectError(boom)
	if err := d.Connect(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Connect: actual=%v expected=%v", err, boom)
	}
	if err := d.Listen(context.Background(), nil); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Listen without connection: actual=%v expected=ErrNoConnection", err)
	}
	if err := d.Push([]byte("x")); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Push without connection: actual=%v expected=ErrNoConnection", err)
	}
}

func Test_F04_FakeDriverCloseIsTerminal(t *testing.T) {
	t.Parallel()
	d := NewFakeDriver(nil)
	ctx := context.Background()
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Push([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Push after Close: actual=%v expected=ErrClosed", err)
	}
	if err := d.Listen(ctx, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Listen after Close: actual=%v expected=ErrClosed", err)
	}
}

func Test_F04_RetryDriverRetriesConnect(t *testing.T) {
	t.Parallel()
	d := NewFakeDriver(nil)
	failing := &flakyDriver{inner: d, failures: 2}
	rd := NewRetryDriver(failing, retry.Policy{
		MaxAttempts: 5,
		Sleep:       func(ctx context.Context, dur time.Duration) error { return nil },
	})
	if err := rd.Connect(context.Background()); err != nil {
		t.Fatalf("RetryDriver.Connect: actual=%v expected=nil", err)
	}
	if failing.attempts.Load() != 3 {
		t.Fatalf("attempts: actual=%d expected=3", failing.attempts.Load())
	}
}

type flakyDriver struct {
	inner    Driver
	failures int64
	attempts atomic.Int64
}

func (f *flakyDriver) Connect(ctx context.Context) error {
	if f.attempts.Add(1) <= f.failures {
		return errors.New("transient connect failure")
	}
	return f.inner.Connect(ctx)
}

func (f *flakyDriver) Listen(ctx context.Context, sink Sink) error { return f.inner.Listen(ctx, sink) }
