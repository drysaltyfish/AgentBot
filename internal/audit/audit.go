// Package audit 实现不可关闭的审计日志（FEATURES.md F-60）。
//
// 审计回答"谁在什么时候让机器人做了什么"：每条记录是一个结构化 JSON 行，
// 通过注入的 io.Writer 落文件或 stdout。写入走有界队列 + 后台 goroutine，
// 队列满或写失败只丢弃并计数，绝不阻塞主流程。
//
// 审计没有开关：构造出 Logger 就会记录。用户内容先经 Redactor 脱敏，再按
// ContentLimit 截断，因此日志中不会出现完整消息或密钥。
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// EventType 是审计事件的类型。
type EventType string

// 支持的审计事件类型；常量取值即落盘字符串。
const (
	EventLLMCall        EventType = "llm_call"
	EventToolCall       EventType = "tool_call"
	EventActionExec     EventType = "action_exec"
	EventVirtualAction  EventType = "virtual_action"
	EventPolicyDenied   EventType = "policy_denied"
	EventApproval       EventType = "approval"
	EventInboundBlocked EventType = "inbound_blocked"
	EventRateLimited    EventType = "rate_limited"
	EventConfigChanged  EventType = "config_changed"
)

// Result 是审计事件的结果。
type Result string

// 支持的结果取值。
const (
	ResultOK     Result = "ok"
	ResultDenied Result = "denied"
	ResultError  Result = "error"
)

// Event 是一条审计记录。At 为零值时由 Logger 的时间源填充。
type Event struct {
	Type       EventType
	At         time.Time
	TraceID    string
	SessionKey string
	UserID     int64
	GroupID    int64
	Action     string
	Result     Result
	DurationMS int64
	Tokens     int
	Params     map[string]string
}

// Options 描述 Logger 的构造参数；零值可用。
type Options struct {
	// Writer 是审计行的去处，nil 表示 os.Stdout。
	Writer io.Writer
	// QueueSize 是有界队列长度，<=0 时取默认值 4096。
	QueueSize int
	// ContentLimit 是脱敏后每个参数值的 rune 上限，<=0 时取默认值 20。
	ContentLimit int
	// Redactor 在截断前对参数值做脱敏；nil 表示恒等。
	Redactor func(string) string
	// Warn 接收丢弃与写失败的告警；nil 表示忽略。
	Warn func(string)
	// Now 提供时间源；nil 表示 time.Now。
	Now func() time.Time
}

// wireEvent 是稳定的落盘结构，字段严格对应 Event，绝不额外添加字段。
type wireEvent struct {
	Time       time.Time         `json:"time"`
	Type       EventType         `json:"type"`
	TraceID    string            `json:"trace_id"`
	SessionKey string            `json:"session_key"`
	UserID     int64             `json:"user_id"`
	GroupID    int64             `json:"group_id"`
	Action     string            `json:"action"`
	Result     Result            `json:"result"`
	DurationMS int64             `json:"duration_ms"`
	Tokens     int               `json:"tokens"`
	Params     map[string]string `json:"params"`
}

// Logger 是异步、有界队列的审计记录器。创建后必须 Close 以排空并停止。
type Logger struct {
	out          io.Writer
	warn         func(string)
	now          func() time.Time
	redactor     func(string) string
	contentLimit int

	mu     sync.RWMutex
	closed bool
	ch     chan []byte
	done   chan struct{}

	dropped atomic.Uint64
}

// New 构造 Logger 并启动后台写入 goroutine。
func New(opts Options) *Logger {
	out := opts.Writer
	if out == nil {
		out = os.Stdout
	}
	size := opts.QueueSize
	if size <= 0 {
		size = 4096
	}
	limit := opts.ContentLimit
	if limit <= 0 {
		limit = 20
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	l := &Logger{
		out:          out,
		warn:         opts.Warn,
		now:          now,
		redactor:     opts.Redactor,
		contentLimit: limit,
		ch:           make(chan []byte, size),
		done:         make(chan struct{}),
	}
	go l.run()
	return l
}

// run 串行消费队列；写失败只计数告警，不重试、不回写。
func (l *Logger) run() {
	defer close(l.done)
	for line := range l.ch {
		if _, err := l.out.Write(line); err != nil {
			l.dropped.Add(1)
			l.warnf("audit: write failed: " + err.Error())
		}
	}
}

// warnf 调用注入的告警回调；在锁外调用，避免回调解锁死锁。
func (l *Logger) warnf(msg string) {
	if l.warn != nil {
		l.warn(msg)
	}
}

// Log 记录一条审计事件：先脱敏编码，再尝试入队。永不阻塞调用方，
// 队列满或已关闭时丢弃并计数。
func (l *Logger) Log(e Event) {
	line := l.encode(e)
	if len(line) == 0 {
		l.dropped.Add(1)
		return
	}
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		l.dropped.Add(1)
		return
	}
	select {
	case l.ch <- line:
		l.mu.RUnlock()
	default:
		l.mu.RUnlock()
		l.dropped.Add(1)
		l.warnf("audit: queue full, record dropped")
	}
}

// encode 把事件编码为单个 JSON 行（含换行）。
func (l *Logger) encode(e Event) []byte {
	at := e.At
	if at.IsZero() {
		at = l.now()
	}
	params := make(map[string]string, len(e.Params))
	for k, v := range e.Params {
		params[k] = l.sanitize(v)
	}
	wire := wireEvent{
		Time:       at,
		Type:       e.Type,
		TraceID:    e.TraceID,
		SessionKey: e.SessionKey,
		UserID:     e.UserID,
		GroupID:    e.GroupID,
		Action:     e.Action,
		Result:     e.Result,
		DurationMS: e.DurationMS,
		Tokens:     e.Tokens,
		Params:     params,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wire); err != nil {
		return nil
	}
	return buf.Bytes()
}

// sanitize 先脱敏再按 rune 截断；被截断时追加原始（脱敏后）长度。
func (l *Logger) sanitize(s string) string {
	if l.redactor != nil {
		s = l.redactor(s)
	}
	runes := []rune(s)
	if len(runes) <= l.contentLimit {
		return s
	}
	return string(runes[:l.contentLimit]) + "…(len=" + strconv.Itoa(len(runes)) + ")"
}

// Dropped 返回因队列满、已关闭或写失败被丢弃的记录数。
func (l *Logger) Dropped() uint64 { return l.dropped.Load() }

// Close 排空队列并停止后台写入；可安全重复调用。ctx 超时返回其错误。
func (l *Logger) Close(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
