package transport

import (
	"context"
	"errors"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/retry"
)

// ErrClosed 表示 Driver 已关闭。
var ErrClosed = errors.New("driver closed")

// Sink 接收一条原始事件与它所属连接的 Caller。
type Sink func(raw []byte, caller Caller)

// Driver 是传输抽象。
//
// Connect 必须返回 error（失败重试策略由上层决定）；Listen 在 ctx 取消后必须
// 立即返回，且返回后不得再调用 sink。
type Driver interface {
	Connect(ctx context.Context) error
	Listen(ctx context.Context, sink Sink) error
}

// Closer 是可关闭的 Driver（可选实现）。
type Closer interface {
	Close(ctx context.Context) error
}

// FakeDriver 是内存事件管道，供契约测试使用。
type FakeDriver struct {
	mu         sync.Mutex
	caller     Caller
	connectErr error
	connected  bool
	closed     bool
	listening  bool
	stopped    bool
	events     chan []byte
}

// NewFakeDriver 构造 FakeDriver；caller 会随事件一起交给 sink。
func NewFakeDriver(caller Caller) *FakeDriver {
	return &FakeDriver{caller: caller, events: make(chan []byte, 64)}
}

// SetConnectError 让下一次 Connect 返回该错误。
func (d *FakeDriver) SetConnectError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connectErr = err
}

// Connect 幂等：已连接时再次调用返回 nil。
func (d *FakeDriver) Connect(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.connectErr != nil {
		return d.connectErr
	}
	if d.closed {
		return ErrClosed
	}
	d.connected = true
	return nil
}

// Push 投递一条原始事件；未连接或已关闭时返回错误。
func (d *FakeDriver) Push(raw []byte) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	if !d.connected {
		d.mu.Unlock()
		return ErrNoConnection
	}
	d.mu.Unlock()
	select {
	case d.events <- raw:
		return nil
	default:
		return errors.New("fake driver queue full")
	}
}

// Listen 消费事件直到 ctx 取消或 Close。
func (d *FakeDriver) Listen(ctx context.Context, sink Sink) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrClosed
	}
	if !d.connected {
		d.mu.Unlock()
		return ErrNoConnection
	}
	d.listening = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.listening = false
		d.stopped = true
		d.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw := <-d.events:
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			sink(raw, d.caller)
		}
	}
}

// Close 关闭管道；其后 Push 返回 ErrClosed，且不再投递事件。
func (d *FakeDriver) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.stopped = true
	return nil
}

// Stopped 报告 Listen 是否已经返回（用于断言"返回后不再调用 sink"）。
func (d *FakeDriver) Stopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// RetryDriver 给任意 Driver 套上可中断的指数退避重试。
//
// **不要接线它——它是两处陷阱，不是可用的重连实现**（第 40 轮查清）：
//
//  1. **`Listen` 不会重新连接**。它只是重试 `next.Listen`，而连接断开后
//     在同一条死连接上再读一次不会有不同结果；它只能把同一个错误重复
//     MaxAttempts 次然后放弃。F-04 要的是"连接失败的重试策略"，
//     那必须把连接**重新建起来**（见下）。
//  2. **尝试次数有上限**。配合 `retry.Default()` 时只有 3 次尝试，
//     之后**永久放弃**——平台晚几分钟回来，进程就再也连不上。
//     F-04 说的"1s 起、最长 30s、带 jitter"描述的是**退避上限**，
//     隐含"一直重试、退避封顶"，而不是"试三次就算了"。
//
// 线上真正在用的是组合根的 `runWSSession`（cmd/server/wssession.go）：
// 它做 `Connect → Listen → Disconnect → 再 Connect`，并且**不设尝试上限**、
// 只复用 `retry.Policy.Delay` 的退避与封顶。这两处陷阱都有测试钉住
// （driver_retry_trap_test.go）：其中一条断言 `Listen` 重试期间 `Connect` 一次都没被调用过。
type RetryDriver struct {
	next   Driver
	policy retry.Policy
}

// NewRetryDriver 构造重试装饰器。
func NewRetryDriver(next Driver, p retry.Policy) *RetryDriver {
	return &RetryDriver{next: next, policy: p}
}

// Connect 反复尝试连接，直到成功、不可重试或 ctx 取消。
func (d *RetryDriver) Connect(ctx context.Context) error {
	_, err := retry.Do(ctx, d.policy, func(ctx context.Context, attempt int) (struct{}, error) {
		return struct{}{}, d.next.Connect(ctx)
	})
	return err
}

// Listen 反复尝试监听，直到 ctx 取消。
func (d *RetryDriver) Listen(ctx context.Context, sink Sink) error {
	_, err := retry.Do(ctx, d.policy, func(ctx context.Context, attempt int) (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, d.next.Listen(ctx, sink)
	})
	return err
}

var (
	_ Driver = (*FakeDriver)(nil)
	_ Driver = (*RetryDriver)(nil)
	_ Closer = (*FakeDriver)(nil)
)
