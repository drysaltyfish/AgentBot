// Package bot 是 AgentBot 的组合根与生命周期（FEATURES.md F-70）。
//
// 关闭顺序与超时的唯一定义在本包；其它 Feature 只把组件挂到对应阶段，
// 不各自实现关闭逻辑（F-23 只负责把"关闭全部 Session"接入 PhaseSession）。
package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	// ErrShutdownTimeout 表示关闭在超时前未完成。
	ErrShutdownTimeout = errors.New("shutdown timed out")
	// ErrAlreadyRunning 表示 Bot 已进入运行态，不能再注册组件。
	ErrAlreadyRunning = errors.New("bot already running")
)

// Phase 定义组件的关闭批次；Shutdown 严格按声明顺序执行。
type Phase int

// 关闭阶段，顺序即执行顺序（F-70 规格的 6 步）。
const (
	// PhaseIntake 停止接收新事件（Driver 停止上报）。
	PhaseIntake Phase = iota
	// PhaseBackground 停止后台任务（ticker、worker、watcher、固化任务）。
	PhaseBackground
	// PhaseSession 关闭会话并固化需要持久的状态。
	PhaseSession
	// PhaseTransport 关闭 Driver 连接。
	PhaseTransport
	// PhaseStorage 刷新日志与指标，关闭存储。
	PhaseStorage
)

// String 实现 fmt.Stringer。
func (p Phase) String() string {
	switch p {
	case PhaseIntake:
		return "intake"
	case PhaseBackground:
		return "background"
	case PhaseSession:
		return "session"
	case PhaseTransport:
		return "transport"
	case PhaseStorage:
		return "storage"
	default:
		return "unknown"
	}
}

var shutdownOrder = []Phase{PhaseIntake, PhaseBackground, PhaseSession, PhaseTransport, PhaseStorage}

// Component 是可被 Bot 关闭的组件。
type Component interface {
	Name() string
	Close(ctx context.Context) error
}

// Bot 持有全部组件与后台 goroutine 的生命周期。
type Bot struct {
	mu              sync.Mutex
	components      map[Phase][]Component
	wg              sync.WaitGroup
	intakeStop      func(ctx context.Context) error
	inflightWait    func(ctx context.Context) error
	shutdownTimeout time.Duration
	inflightTimeout time.Duration
	running         bool
	once            sync.Once
	shutdownErr     error
	closedNames     []string

	// baseCtx 是全部后台 goroutine 的父上下文，在第 3 步（停止后台任务）被取消。
	baseCtx    context.Context
	baseCancel context.CancelFunc
}

// Option 配置 Bot。
type Option func(*Bot)

// WithShutdownTimeout 设置关闭总超时（默认 10s）。
func WithShutdownTimeout(d time.Duration) Option {
	return func(b *Bot) {
		if d > 0 {
			b.shutdownTimeout = d
		}
	}
}

// WithInflightTimeout 设置"等待在途事件"的超时（默认与关闭总超时一致）。
func WithInflightTimeout(d time.Duration) Option {
	return func(b *Bot) {
		if d > 0 {
			b.inflightTimeout = d
		}
	}
}

// WithIntakeStop 设置第 1 步"停止接收新事件"的钩子。
func WithIntakeStop(fn func(ctx context.Context) error) Option {
	return func(b *Bot) { b.intakeStop = fn }
}

// WithInflightWait 自定义第 2 步"等待在途工作"的实现。
//
// 不设置时退化为等待全部 Go() 启动的 goroutine 退出；组合根通常需要更精确的
// 语义（例如只等未完成的回复任务），此时用它覆盖。
func WithInflightWait(fn func(ctx context.Context) error) Option {
	return func(b *Bot) { b.inflightWait = fn }
}

// New 构造 Bot；默认关闭超时 10s。
func New(opts ...Option) *Bot {
	baseCtx, baseCancel := context.WithCancel(context.Background())
	b := &Bot{
		components:      make(map[Phase][]Component),
		shutdownTimeout: 10 * time.Second,
		inflightTimeout: 0,
		baseCtx:         baseCtx,
		baseCancel:      baseCancel,
	}
	for _, o := range opts {
		o(b)
	}
	if b.inflightTimeout <= 0 {
		b.inflightTimeout = b.shutdownTimeout
	}
	return b
}

// ShutdownTimeout 返回当前配置的关闭总超时。
func (b *Bot) ShutdownTimeout() time.Duration { return b.shutdownTimeout }

// Context 返回全部后台 goroutine 共享的上下文；它在第 3 步关闭阶段被取消。
func (b *Bot) Context() context.Context { return b.baseCtx }

// MarkRunning 之后不再允许注册组件（避免关到一半又冒出新组件）。
func (b *Bot) MarkRunning() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = true
}

// Register 把组件挂到某个关闭阶段。运行后再注册返回 ErrAlreadyRunning。
func (b *Bot) Register(phase Phase, c Component) error {
	if c == nil {
		return errors.New("register nil component")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return fmt.Errorf("register %q: %w", c.Name(), ErrAlreadyRunning)
	}
	b.components[phase] = append(b.components[phase], c)
	return nil
}

// Go 在 Bot 的生命周期内启动一个后台 goroutine。
//
// 传入的 ctx 来自 Bot.Context()，在第 3 步（停止后台任务）被取消；Shutdown 会等待
// 它退出。后台 goroutine 的 panic 不会杀死进程。
func (b *Bot) Go(name string, fn func(ctx context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			_ = recover()
		}()
		_ = name
		fn(b.baseCtx)
	}()
}

// ClosedComponents 返回已被关闭的组件名（按关闭顺序），便于测试与启动日志。
func (b *Bot) ClosedComponents() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.closedNames))
	copy(out, b.closedNames)
	return out
}

// Shutdown 幂等：重复调用返回与第一次相同的结果。
func (b *Bot) Shutdown(ctx context.Context) error {
	b.once.Do(func() {
		b.shutdownErr = b.doShutdown(ctx)
	})
	return b.shutdownErr
}

func (b *Bot) doShutdown(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, b.shutdownTimeout)
	defer cancel()

	var errs []error
	var unfinished []string

	// 1. 停止接收新事件。
	b.mu.Lock()
	stop := b.intakeStop
	b.mu.Unlock()
	if stop != nil {
		if err := stop(cctx); err != nil {
			errs = append(errs, fmt.Errorf("phase %s: stop intake: %w", PhaseIntake, err))
			unfinished = append(unfinished, "intake-stop")
		}
	}

	// 2. 等待在途事件处理完成。
	if err := b.waitInflight(cctx); err != nil {
		errs = append(errs, fmt.Errorf("phase %s: %w", PhaseIntake, err))
		unfinished = append(unfinished, "in-flight handlers")
	}

	// 3. 停止后台任务：先取消父上下文，再等 goroutine 退出，最后关后台组件。
	b.baseCancel()
	if err := b.waitBackground(cctx); err != nil {
		errs = append(errs, fmt.Errorf("phase %s: %w", PhaseBackground, err))
		unfinished = append(unfinished, "background goroutines")
	}

	// 3..6. 各批次组件逆序关闭。
	for _, ph := range shutdownOrder[1:] {
		for _, c := range b.componentsIn(ph) {
			if err := c.Close(cctx); err != nil {
				errs = append(errs, fmt.Errorf("phase %s: close %s: %w", ph, c.Name(), err))
				unfinished = append(unfinished, c.Name())
				continue
			}
			b.mu.Lock()
			b.closedNames = append(b.closedNames, c.Name())
			b.mu.Unlock()
		}
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(unfinished)
	joined := errors.Join(errs...)
	if cctx.Err() != nil {
		return fmt.Errorf("%w: unfinished=%v: %w", ErrShutdownTimeout, unfinished, joined)
	}
	return fmt.Errorf("unfinished=%v: %w", unfinished, joined)
}

func (b *Bot) componentsIn(ph Phase) []Component {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.components[ph]
	out := make([]Component, len(src))
	copy(out, src)
	return out
}

func (b *Bot) waitInflight(ctx context.Context) error {
	if b.inflightWait != nil {
		return b.inflightWait(ctx)
	}
	return b.waitBackground(ctx)
}

// waitBackground 等待全部 Go() 启动的 goroutine 退出，受 ctx 与超时双重约束。
func (b *Bot) waitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()

	timer := time.NewTimer(b.inflightTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("goroutines did not finish within %s", b.inflightTimeout)
	}
}
