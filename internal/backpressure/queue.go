// Package backpressure 提供有界的事件队列：洪峰时丢弃并计数，绝不阻塞读取方（F-20）。
//
// 为什么不能每个事件起一个 goroutine：群被刷屏或批量撤回时，那会把内存与调度压垮，
// 而被拖垮的是"平台连接的读循环"——最不能卡的地方。
package backpressure

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Policy 是队列满时的处理策略。
type Policy int

const (
	// DropNewest 拒绝新事件并计数（默认）。
	DropNewest Policy = iota
	// DropOldest 覆盖最旧的事件。
	DropOldest
	// Block 阻塞提交方（只适合能接受反压的调用方）。
	Block
)

// Stats 是队列的运行计数。
type Stats struct {
	Received  uint64
	Dropped   uint64
	Processed uint64
	Depth     int
}

// Options 配置队列。
type Options struct {
	// Capacity 是队列容量；<=0 时取 1024。
	Capacity int
	// Workers 是 worker 数；<=0 时取 max(4, GOMAXPROCS)。
	Workers int
	// Policy 是满队列策略；零值为 DropNewest。
	Policy Policy
	// DrainTimeout 是关闭时等待排空的上限；<=0 时取 3s。
	DrainTimeout time.Duration
	// OnDrop 在丢弃时回调，reason 取值 queue_full / drop_oldest / closed。
	OnDrop func(reason string)
	// OnWait 在出队处理前回调，参数是排队等待时长（用于观测积压）。
	OnWait func(d time.Duration)
	// OnPanic 在 handler panic（已被恢复）时回调。
	OnPanic func(recovered any)
}

type queued[T any] struct {
	item T
	at   time.Time
}

// Queue 是泛型有界队列。
type Queue[T any] struct {
	handler func(T)
	opts    Options
	ch      chan queued[T]
	done    chan struct{}
	closed  atomic.Bool
	wg      sync.WaitGroup

	received  atomic.Uint64
	dropped   atomic.Uint64
	processed atomic.Uint64
}

// New 构造队列；handler 由 worker 调用。
func New[T any](handler func(T), opts Options) *Queue[T] {
	if opts.Capacity <= 0 {
		opts.Capacity = 1024
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
		if n := runtime.GOMAXPROCS(0); n > opts.Workers {
			opts.Workers = n
		}
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 3 * time.Second
	}
	return &Queue[T]{
		handler: handler,
		opts:    opts,
		ch:      make(chan queued[T], opts.Capacity),
		done:    make(chan struct{}),
	}
}

// Start 起 worker 池。
func (q *Queue[T]) Start() {
	for i := 0; i < q.opts.Workers; i++ {
		q.wg.Add(1)
		go q.work()
	}
}

func (q *Queue[T]) work() {
	defer q.wg.Done()
	for {
		select {
		case j := <-q.ch:
			q.process(j)
		case <-q.done:
			// 关闭信号：把队列里剩下的处理完再退出（明确的排空语义）。
			for {
				select {
				case j := <-q.ch:
					q.process(j)
				default:
					return
				}
			}
		}
	}
}

func (q *Queue[T]) process(j queued[T]) {
	if q.opts.OnWait != nil {
		q.opts.OnWait(time.Since(j.at))
	}
	defer func() {
		if r := recover(); r != nil {
			// worker 必须活下来：一次 handler panic 不能让整条链路停摆。
			if q.opts.OnPanic != nil {
				q.opts.OnPanic(r)
			}
		}
	}()
	q.handler(j.item)
	q.processed.Add(1)
}

// Submit 入队；返回 false 表示被丢弃（队列满或已关闭）。
func (q *Queue[T]) Submit(item T) bool {
	if q.closed.Load() {
		q.drop("closed")
		return false
	}
	j := queued[T]{item: item, at: time.Now()}

	switch q.opts.Policy {
	case Block:
		select {
		case q.ch <- j:
			q.received.Add(1)
			return true
		case <-q.done:
			q.drop("closed")
			return false
		}
	case DropOldest:
		select {
		case q.ch <- j:
			q.received.Add(1)
			return true
		default:
			// 腾一个位置：先挤掉最旧的那条，再放新的。
			select {
			case <-q.ch:
				q.drop("drop_oldest")
			default:
			}
			select {
			case q.ch <- j:
				q.received.Add(1)
				return true
			default:
				q.drop("queue_full")
				return false
			}
		}
	case DropNewest:
		select {
		case q.ch <- j:
			q.received.Add(1)
			return true
		default:
			q.drop("queue_full")
			return false
		}
	default:
		// 未知策略按最保守的方式处理：拒绝新事件而不是阻塞。
		q.drop("queue_full")
		return false
	}
}

func (q *Queue[T]) drop(reason string) {
	q.dropped.Add(1)
	if q.opts.OnDrop != nil {
		q.opts.OnDrop(reason)
	}
}

// Depth 返回当前队列深度。
func (q *Queue[T]) Depth() int { return len(q.ch) }

// Stats 返回计数快照。
func (q *Queue[T]) Stats() Stats {
	return Stats{
		Received:  q.received.Load(),
		Dropped:   q.dropped.Load(),
		Processed: q.processed.Load(),
		Depth:     len(q.ch),
	}
}

// Close 停止接收并等待排空；超过 DrainTimeout 返回错误。幂等。
func (q *Queue[T]) Close(ctx context.Context) error {
	if !q.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(q.done)

	finished := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(finished)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, q.opts.DrainTimeout)
	defer cancel()
	select {
	case <-finished:
		return nil
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}
