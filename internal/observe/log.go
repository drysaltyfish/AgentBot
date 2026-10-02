// Package observe 提供结构化日志与追踪上下文（FEATURES.md F-67）。
//
// 高并发下日志不能成为瓶颈：写入走有界队列 + 丢弃计数，绝不阻塞调用方。
package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// ErrClosed 表示 logger 已关闭。
var ErrClosed = errors.New("logger closed")

// TraceIDKey 是日志与 ctx 中 trace 标识的键名。
const TraceIDKey = "trace_id"

type traceIDCtxKey struct{}

// WithTraceID 把 trace_id 放进 ctx；同一次事件的所有日志都应带上它。
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDCtxKey{}, id)
}

// TraceID 取出 ctx 里的 trace_id，没有则返回空串。
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// Options 描述 logger 的构造参数。
type Options struct {
	Level        string
	Format       string
	Components   map[string]string
	QueueSize    int
	Writer       io.Writer
	DebugContent bool
	// Redactor 在每条日志落盘前做脱敏（F-61 在 M3 注入；默认恒等）。
	Redactor func(string) string
}

// Logger 是带组件级级别覆盖、异步写入与内容门控的 slog 封装。
type Logger struct {
	queue  *queueWriter
	root   *slog.Logger
	levels map[string]string
	format string
	debug  bool
}

type queueWriter struct {
	ch      chan []byte
	out     io.Writer
	dropped atomic.Uint64
	wg      sync.WaitGroup
	once    sync.Once
	closed  atomic.Bool
}

func newQueueWriter(out io.Writer, size int) *queueWriter {
	if size <= 0 {
		size = 1024
	}
	q := &queueWriter{ch: make(chan []byte, size), out: out}
	q.wg.Add(1)
	go q.run()
	return q
}

func (q *queueWriter) run() {
	defer q.wg.Done()
	for line := range q.ch {
		if _, err := q.out.Write(line); err != nil {
			// 写失败不能再写日志（会递归）；直接丢弃并计数。
			q.dropped.Add(1)
		}
	}
}

// Write 永不阻塞：队列满时丢弃并计数。
func (q *queueWriter) Write(p []byte) (int, error) {
	if q.closed.Load() {
		q.dropped.Add(1)
		return len(p), nil
	}
	buf := make([]byte, len(p))
	copy(buf, p)
	select {
	case q.ch <- buf:
	default:
		q.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped 返回被丢弃的日志条数。
func (q *queueWriter) Dropped() uint64 { return q.dropped.Load() }

func (q *queueWriter) Close(ctx context.Context) error {
	q.once.Do(func() {
		q.closed.Store(true)
		close(q.ch)
	})
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type redactingWriter struct {
	next     io.Writer
	redactor func(string) string
}

func (w redactingWriter) Write(p []byte) (int, error) {
	if w.redactor == nil {
		return w.next.Write(p)
	}
	out := w.redactor(string(p))
	if _, err := w.next.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// New 构造 Logger。返回的 Logger 一旦创建就必须在退出时 Close。
func New(opts Options) *Logger {
	out := opts.Writer
	if out == nil {
		out = os.Stdout
	}
	format := opts.Format
	if format != "text" {
		format = "json"
	}
	l := &Logger{
		queue:  newQueueWriter(redactingWriter{next: out, redactor: opts.Redactor}, opts.QueueSize),
		levels: opts.Components,
		format: format,
		debug:  opts.DebugContent,
	}
	l.root = slog.New(l.handlerFor(parseLevel(opts.Level)))
	return l
}

// handlerFor 为给定级别新建 handler。组件级覆盖因此既能调高也能调低级别。
func (l *Logger) handlerFor(level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if l.format == "text" {
		return slog.NewTextHandler(l.queue, opts)
	}
	return slog.NewJSONHandler(l.queue, opts)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Component 返回带 component 属性、且可被组件级覆盖的 logger。
func (l *Logger) Component(name string) *slog.Logger {
	level := parseLevel(l.levels[name])
	return slog.New(l.handlerFor(level)).With("component", name)
}

// WithContext 把 ctx 里的 trace_id 挂到 logger 上。
func WithContext(ctx context.Context, lg *slog.Logger) *slog.Logger {
	if id := TraceID(ctx); id != "" {
		return lg.With(TraceIDKey, id)
	}
	return lg
}

// Content 是消息内容的门控（F-67 边界）：关闭 debug_content 时只记长度摘要，
// 绝不把用户消息全文写进日志。
func (l *Logger) Content(s string) string {
	if l.debug {
		return s
	}
	return fmt.Sprintf("<omitted len=%d>", len([]rune(s)))
}

// DebugContent 返回是否允许记录消息全文。
func (l *Logger) DebugContent() bool { return l.debug }

// Dropped 返回因队列满被丢弃的日志条数。
func (l *Logger) Dropped() uint64 { return l.queue.Dropped() }

// Close 排空队列并停止后台写入；可重复调用。
func (l *Logger) Close(ctx context.Context) error { return l.queue.Close(ctx) }

// With 返回带额外属性的根 logger。
func (l *Logger) With(args ...any) *slog.Logger { return l.root.With(args...) }

// Debug 记录 debug 级别日志。
func (l *Logger) Debug(msg string, args ...any) { l.root.Debug(msg, args...) }

// Info 记录 info 级别日志。
func (l *Logger) Info(msg string, args ...any) { l.root.Info(msg, args...) }

// Warn 记录 warn 级别日志。
func (l *Logger) Warn(msg string, args ...any) { l.root.Warn(msg, args...) }

// Error 记录 error 级别日志。
func (l *Logger) Error(msg string, args ...any) { l.root.Error(msg, args...) }

// Recover 是给 defer 用的 panic 兜底：记录堆栈并把 panic 转成 error。
func Recover(lg *slog.Logger, component string, r any, target *error) {
	if r == nil {
		return
	}
	stack := debug.Stack()
	if lg != nil {
		lg.Error("recovered panic", "component", component, "panic", fmt.Sprint(r), "stack", string(stack))
	}
	if target != nil && *target == nil {
		*target = fmt.Errorf("panic in %s: %v", component, r)
	}
}
