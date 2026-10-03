package llm

import (
	"context"
	"strings"
	"time"
)

// F-64 的默认阈值。
const (
	// DefaultFlushChars 是触发发送的长度阈值（字符数）。
	DefaultFlushChars = 40
	// DefaultFlushInterval 同时是时间阈值与发送频率上限（1 次 / 800ms）。
	DefaultFlushInterval = 800 * time.Millisecond
	// DefaultFirstMinChars 是首段额外延迟：第一次发送至少要有这么多字符，
	// 避免把"你"这样的半个字先抛出去（句末标点与流结束不受此限）。
	DefaultFirstMinChars = 8
)

// FlushKind 描述一次增量发送的语义。
type FlushKind string

const (
	// FlushDelta 表示新增片段，接收方应追加。
	FlushDelta FlushKind = "delta"
	// FlushReplace 表示模型改写，接收方应用 Full 替换整条消息（需平台支持编辑）。
	FlushReplace FlushKind = "replace"
	// FlushFinal 是流结束时的收尾发送。
	FlushFinal FlushKind = "final"
	// FlushHalt 表示增量中止（追加式平台遇到重写，或流因错误中断）。
	FlushHalt FlushKind = "halt"
)

// StreamFlush 是一次待发送的增量。
type StreamFlush struct {
	Kind FlushKind
	// Delta 是相对上次已发送前缀的新增部分；FlushReplace 时为空。
	Delta string
	// Full 是当前累积全文；替换场景使用。
	Full string
	// First 标记本流首次发送（接收方可附带"正在输入…"提示）。
	First bool
	// Interrupted 标记流因错误中断；已发送内容不回滚。
	Interrupted bool
}

// StreamFlushFunc 是注入的发送回调；由组合根接到 outbound。
type StreamFlushFunc func(StreamFlush)

// StreamConfig 配置 StreamSplitter。
//
// 所有时间相关的行为都由注入的 Now 驱动，因此测试不需要 sleep。
type StreamConfig struct {
	// MaxChars 是长度阈值；<=0 用 DefaultFlushChars。
	MaxChars int
	// MaxInterval 是"距上次发送超过此时长即触发"；<=0 用 DefaultFlushInterval。
	MaxInterval time.Duration
	// MinInterval 是发送频率上限；<=0 用 DefaultFlushInterval。超限时合并到下次。
	MinInterval time.Duration
	// FirstMinChars 是首段最小长度；<=0 用 DefaultFirstMinChars。
	FirstMinChars int
	// EditMessages 为 true 时，改写以 FlushReplace（整条替换）表达；
	// 否则改写会让追加式平台中止该流，绝不发送重复/错乱文本。
	EditMessages bool
	// Now 注入时钟；nil 时用 time.Now。
	Now func() time.Time
	// Flush 是发送回调。
	Flush StreamFlushFunc
	// OnWarn 接收异常事件（如改写中止）。
	OnWarn func(string)
}

// StreamSplitter 把流式输出累积成缓冲区，并按句末标点/长度/时间触发增量发送。
//
// 发送内容恒为"当前累积全文相对上次已发送前缀的新增部分"，只读模型流、不触碰
// ChatRequest，因此不会破坏请求侧的前缀缓存（F-64/F-65）。
//
// 非并发安全：一条流由一个 goroutine 顺序驱动。
type StreamSplitter struct {
	cfg       StreamConfig
	full      []rune
	sent      int
	lastFlush time.Time
	started   bool
	done      bool
	halted    bool
}

// 反引号用数值表示，避免源码里出现代码围栏字符。
const backtick = rune(96)

// NewStreamSplitter 构造切分器并填默认值。
func NewStreamSplitter(cfg StreamConfig) *StreamSplitter {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = DefaultFlushChars
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = DefaultFlushInterval
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = DefaultFlushInterval
	}
	if cfg.FirstMinChars <= 0 {
		cfg.FirstMinChars = DefaultFirstMinChars
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &StreamSplitter{cfg: cfg, lastFlush: cfg.Now()}
}

// Feed 处理一个流式分片：累积 Content、在 Done/Err 时收尾。
func (s *StreamSplitter) Feed(c Chunk) {
	if s.done || s.halted {
		return
	}
	if c.Content != "" {
		s.FeedDelta(c.Content)
	}
	if c.Err != nil {
		s.finish(true)
		return
	}
	if c.Done {
		s.Finish()
	}
}

// FeedDelta 追加一段模型增量。
func (s *StreamSplitter) FeedDelta(delta string) {
	if s.done || s.halted || delta == "" {
		return
	}
	s.full = append(s.full, []rune(delta)...)
	s.maybeFlush()
}

// FeedFull 用一份完整快照更新缓冲。
//
// 若新快照不是旧快照的扩展（模型回退/重写），按 EditMessages 选择整条替换或中止。
func (s *StreamSplitter) FeedFull(full string) {
	if s.done || s.halted {
		return
	}
	old := string(s.full)
	if strings.HasPrefix(full, old) {
		if extra := full[len(old):]; extra != "" {
			s.full = append(s.full, []rune(extra)...)
		}
		s.maybeFlush()
		return
	}
	s.handleRewrite(full)
}

// Tick 让时间阈值在没有新分片时也能生效；调用方可由定时器驱动。
func (s *StreamSplitter) Tick() {
	if s.done || s.halted {
		return
	}
	s.maybeFlush()
}

// Finish 结束流：把剩余缓冲全部发出；已发送内容不回滚。
func (s *StreamSplitter) Finish() { s.finish(false) }

func (s *StreamSplitter) finish(interrupted bool) {
	if s.done || s.halted {
		return
	}
	s.done = true
	if s.sent < len(s.full) {
		s.emit(len(s.full), FlushFinal, interrupted)
		return
	}
	if interrupted && s.cfg.Flush != nil {
		s.cfg.Flush(StreamFlush{Kind: FlushHalt, Full: string(s.full), Interrupted: true})
	}
}

// Full 返回当前累积全文。
func (s *StreamSplitter) Full() string { return string(s.full) }

// Pending 返回尚未发送的缓冲。
func (s *StreamSplitter) Pending() string {
	if s.sent >= len(s.full) {
		return ""
	}
	return string(s.full[s.sent:])
}

// Done 报告流是否已结束。
func (s *StreamSplitter) Done() bool { return s.done }

// Halted 报告增量是否已中止。
func (s *StreamSplitter) Halted() bool { return s.halted }

// ConsumeStream 顺序消费一条分片流，直到 channel 关闭或 ctx 取消；结束时补发剩余。
func (s *StreamSplitter) ConsumeStream(ctx context.Context, ch <-chan Chunk) {
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-ch:
			if !ok {
				s.Finish()
				return
			}
			s.Feed(c)
		}
	}
}

func (s *StreamSplitter) maybeFlush() {
	if s.done || s.halted {
		return
	}
	end, ok := s.decide()
	if !ok {
		return
	}
	now := s.cfg.Now()
	// 平台限流：距上次发送不足 MinInterval 时合并到下次（首次发送不受限）。
	if s.started && now.Sub(s.lastFlush) < s.cfg.MinInterval {
		return
	}
	s.emit(end, FlushDelta, false)
}

// decide 返回本次应发送到的绝对位置；第二个返回值为 false 表示不触发。
func (s *StreamSplitter) decide() (int, bool) {
	if s.sent >= len(s.full) {
		return 0, false
	}
	// 标点边界优先：只发到最后一个句末标点（含），不在代码围栏内切分。
	if end := s.lastBoundary(s.sent); end > s.sent {
		return end, true
	}
	unsent := len(s.full) - s.sent
	if unsent >= s.cfg.MaxChars && unsent >= s.cfg.FirstMinChars {
		return len(s.full), true
	}
	if s.cfg.Now().Sub(s.lastFlush) >= s.cfg.MaxInterval && unsent >= s.cfg.FirstMinChars {
		return len(s.full), true
	}
	return 0, false
}

func (s *StreamSplitter) emit(end int, kind FlushKind, interrupted bool) {
	if end <= s.sent {
		return
	}
	f := StreamFlush{
		Kind:        kind,
		Delta:       string(s.full[s.sent:end]),
		Full:        string(s.full),
		First:       !s.started,
		Interrupted: interrupted,
	}
	s.sent = end
	s.started = true
	s.lastFlush = s.cfg.Now()
	if s.cfg.Flush != nil {
		s.cfg.Flush(f)
	}
}

func (s *StreamSplitter) handleRewrite(full string) {
	// 尚未发送过任何内容时改写是安全的：直接采纳新版本。
	if !s.started {
		s.full = []rune(full)
		s.sent = 0
		s.maybeFlush()
		return
	}
	if s.cfg.EditMessages {
		s.full = []rune(full)
		s.sent = len(s.full)
		s.started = true
		s.lastFlush = s.cfg.Now()
		if s.cfg.Flush != nil {
			s.cfg.Flush(StreamFlush{Kind: FlushReplace, Full: full, First: false})
		}
		return
	}
	// 追加式平台无法撤回已发送内容：停止增量，绝不发送错乱文本。
	s.halted = true
	if s.cfg.OnWarn != nil {
		s.cfg.OnWarn("stream content rewritten; incremental sending halted (platform cannot edit messages)")
	}
	if s.cfg.Flush != nil {
		s.cfg.Flush(StreamFlush{Kind: FlushHalt, Full: full, Interrupted: true})
	}
}

// lastBoundary 返回 [from, len) 内最后一个"围栏外句末标点"之后的位置；无则 -1。
func (s *StreamSplitter) lastBoundary(from int) int {
	fence := false
	for i := 0; i+2 < from; i++ {
		if s.full[i] == backtick && s.full[i+1] == backtick && s.full[i+2] == backtick {
			fence = !fence
			i += 2
		}
	}
	best := -1
	for i := from; i < len(s.full); i++ {
		if i+2 < len(s.full) && s.full[i] == backtick && s.full[i+1] == backtick && s.full[i+2] == backtick {
			fence = !fence
			i += 2
			continue
		}
		if fence {
			continue
		}
		if isSentenceBoundary(s.full[i]) {
			best = i + 1
		}
	}
	return best
}

func isSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '.', '!', '?', '\n':
		return true
	default:
		return false
	}
}
