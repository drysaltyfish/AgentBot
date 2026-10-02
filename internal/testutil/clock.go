// Package testutil 提供手写 fake，目标是让核心链路的单测不需要网络、不需要
// sleep（FEATURES.md F-76）。
//
// 约定：
//   - 本包内每个 fake 都必须并发安全；
//   - fake 的断言失败信息必须同时包含"实际"与"期望"；
//   - 每个 fake 与被 fake 的接口用编译期断言绑定。
package testutil

import (
	"sync"
	"time"
)

// Clock 是生产代码应当依赖的时间接口，而不是直接调用 time.Now。
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock 是真实时钟。
type SystemClock struct{}

// Now 返回当前时间。
func (SystemClock) Now() time.Time { return time.Now() }

// After 返回一个在 d 之后触发的 channel。
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// FakeClock 是手动推进的时钟，用于确定性地测试超时与 TTL。
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

// NewFakeClock 以给定的起始时间创建 FakeClock；零值时间会被替换为 Unix 纪元。
func NewFakeClock(start time.Time) *FakeClock {
	if start.IsZero() {
		start = time.Unix(0, 0).UTC()
	}
	return &FakeClock{now: start}
}

// Now 返回当前（虚拟）时间。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After 在虚拟时间推进到 now+d 时向返回的 channel 发送一次时间。
//
// d <= 0 时立即发送（channel 带缓冲，调用方无需先接收）。
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	ch := make(chan time.Time, 1)
	at := c.now.Add(d)
	if !at.After(c.now) {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: at, ch: ch})
	return ch
}

// Advance 把虚拟时间前进 d，并唤醒所有到期的等待者。
func (c *FakeClock) Advance(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.fireLocked()
}

// Set 直接设置虚拟时间（允许回拨，用于测试时间回拨场景）。
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	c.fireLocked()
}

// PendingWaiters 返回尚未触发的等待者数量，便于断言没有泄漏。
func (c *FakeClock) PendingWaiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func (c *FakeClock) fireLocked() {
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			select {
			case w.ch <- c.now:
			default:
			}
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
}

var (
	_ Clock = (*FakeClock)(nil)
	_ Clock = SystemClock{}
)
