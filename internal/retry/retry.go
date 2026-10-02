// Package retry 提供可被 ctx 中断的指数退避重试（FEATURES.md F-30 的可复用内核）。
package retry

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// ErrAttemptsExhausted 表示所有尝试都用完了。
var ErrAttemptsExhausted = errors.New("retry attempts exhausted")

// Policy 描述退避策略。
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Factor      float64
	Jitter      bool
	// Retryable 判定某个错误是否值得重试；nil 表示全部可重试。
	Retryable func(error) bool
	// Sleep 用于测试注入；nil 时使用真实定时器（仍然可被 ctx 中断）。
	Sleep func(ctx context.Context, d time.Duration) error
}

// Default 返回 F-30 规定的默认策略。
func Default() Policy {
	return Policy{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
		Factor:      2,
		Jitter:      true,
	}
}

// Normalize 补齐零值，保证 Do 的行为可预期。
func (p Policy) Normalize() Policy {
	d := Default()
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = d.MaxAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = d.BaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = d.MaxDelay
	}
	if p.Factor <= 0 {
		p.Factor = d.Factor
	}
	return p
}

// Delay 返回第 attempt 次重试（从 1 开始）前应等待的时长。
func (p Policy) Delay(attempt int) time.Duration {
	p = p.Normalize()
	if attempt < 1 {
		attempt = 1
	}
	d := float64(p.BaseDelay) * math.Pow(p.Factor, float64(attempt-1))
	if d > float64(p.MaxDelay) {
		d = float64(p.MaxDelay)
	}
	if p.Jitter {
		// 全抖动：在 [d/2, d) 之间取值，避免重试风暴同步。
		d = d/2 + rand.Float64()*(d/2)
	}
	return time.Duration(d)
}

// Do 反复调用 fn 直到成功、不可重试或尝试用尽。
//
// attempt 从 1 开始。返回的 error 在尝试用尽时 wrap ErrAttemptsExhausted。
func Do[T any](ctx context.Context, p Policy, fn func(ctx context.Context, attempt int) (T, error)) (T, error) {
	p = p.Normalize()
	var zero T
	var lastErr error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		v, err := fn(ctx, attempt)
		if err == nil {
			return v, nil
		}
		lastErr = err
		if p.Retryable != nil && !p.Retryable(err) {
			return zero, err
		}
		if attempt == p.MaxAttempts {
			break
		}
		if err := sleep(ctx, p, attempt); err != nil {
			return zero, err
		}
	}
	return zero, errors.Join(ErrAttemptsExhausted, lastErr)
}

func sleep(ctx context.Context, p Policy, attempt int) error {
	d := p.Delay(attempt)
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
