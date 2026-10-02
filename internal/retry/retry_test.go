package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

func Test_F30_SucceedsAfterRetries(t *testing.T) {
	t.Parallel()
	calls := 0
	got, err := Do(context.Background(), Policy{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Factor: 2,
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	}, func(ctx context.Context, attempt int) (string, error) {
		calls++
		if attempt < 3 {
			return "", errBoom
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do: actual=%v expected=nil", err)
	}
	if got != "ok" {
		t.Fatalf("value: actual=%q expected=%q", got, "ok")
	}
	if calls != 3 {
		t.Fatalf("attempts: actual=%d expected=3", calls)
	}
}

func Test_F30_NonRetryableStopsImmediately(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := Do(context.Background(), Policy{
		MaxAttempts: 5, Retryable: func(error) bool { return false },
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	}, func(ctx context.Context, attempt int) (int, error) {
		calls++
		return 0, errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error: actual=%v expected=boom", err)
	}
	if calls != 1 {
		t.Fatalf("attempts: actual=%d expected=1", calls)
	}
}

func Test_F30_ContextCancellationInterruptsBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := Do(ctx, Policy{MaxAttempts: 3, BaseDelay: time.Hour, MaxDelay: time.Hour, Factor: 2},
		func(ctx context.Context, attempt int) (int, error) { return 0, errBoom })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: actual=%v expected=context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation was not honoured: actual=%v expected=immediate", elapsed)
	}
}

func Test_F30_ExhaustionWrapsSentinel(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := Do(context.Background(), Policy{
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	}, func(ctx context.Context, attempt int) (int, error) {
		calls++
		return 0, errBoom
	})
	if !errors.Is(err, ErrAttemptsExhausted) {
		t.Fatalf("error should wrap ErrAttemptsExhausted: actual=%v", err)
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("error should wrap the last error: actual=%v", err)
	}
	if calls != 3 {
		t.Fatalf("attempts: actual=%d expected=3", calls)
	}
}

func Test_F30_DelayIsCappedAndJittered(t *testing.T) {
	t.Parallel()
	p := Policy{MaxAttempts: 10, BaseDelay: time.Second, MaxDelay: 3 * time.Second, Factor: 2, Jitter: false}
	if got := p.Delay(1); got != time.Second {
		t.Fatalf("delay(1): actual=%v expected=1s", got)
	}
	if got := p.Delay(2); got != 2*time.Second {
		t.Fatalf("delay(2): actual=%v expected=2s", got)
	}
	if got := p.Delay(9); got != 3*time.Second {
		t.Fatalf("delay(9) should be capped: actual=%v expected=3s", got)
	}

	j := Policy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 10 * time.Second, Factor: 2, Jitter: true}
	for i := 0; i < 50; i++ {
		d := j.Delay(1)
		if d < 500*time.Millisecond || d >= time.Second {
			t.Fatalf("jittered delay out of [d/2, d): actual=%v", d)
		}
	}
}
