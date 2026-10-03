package outbound

import (
	"context"
	"errors"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

// ErrStreamHalted 表示增量发送已被中止。
var ErrStreamHalted = errors.New("incremental stream halted")

// StreamOptions 配置 StreamSender。
type StreamOptions struct {
	// TypingHint 非空时，在首次正文片段前单独发一条提示（平台支持时可留空）。
	TypingHint string
	// EditMessages 为 true 且 Edit 非 nil 时，FlushReplace 交给 Edit；否则中止该流。
	EditMessages bool
	// Edit 是"编辑已发送消息"的能力；平台无此能力时保持 nil。
	Edit func(ctx context.Context, target Target, full string) error
	// OnHalt 接收中止原因（可为 nil）。
	OnHalt func(reason string)
}

// StreamSender 把 llm 的增量 flush 桥接到统一出口 Sender。
//
// 每次发送仍走 Sender.Send，因此出口过滤链与审计不会被绕过。发送频率已在
// llm.StreamSplitter 里限流并合并，这里只负责落地发送与重写处理。
type StreamSender struct {
	sender  *Sender
	target  Target
	opts    StreamOptions
	hinted  bool
	sends   int
	halted  bool
	haltErr error
}

// NewStreamSender 构造增量发送器。
func NewStreamSender(sender *Sender, target Target, opts StreamOptions) *StreamSender {
	return &StreamSender{sender: sender, target: target, opts: opts}
}

// Handler 返回绑定了 ctx 的 flush 回调，便于组合根一行接线到 StreamSplitter。
func (s *StreamSender) Handler(ctx context.Context) llm.StreamFlushFunc {
	return func(f llm.StreamFlush) { s.OnFlush(ctx, f) }
}

// OnFlush 处理一次增量发送事件。
func (s *StreamSender) OnFlush(ctx context.Context, f llm.StreamFlush) {
	if s.halted {
		return
	}
	switch f.Kind {
	case llm.FlushDelta, llm.FlushFinal:
		if f.Delta == "" {
			return
		}
		s.send(ctx, f.Delta, f.First)
	case llm.FlushReplace:
		s.replace(ctx, f.Full)
	case llm.FlushHalt:
		s.halt("stream halted")
	default:
		// 未知类型向前兼容：忽略，不影响已发送内容。
	}
}

// Sends 返回已发送的正文条数。
func (s *StreamSender) Sends() int { return s.sends }

// Halted 报告是否已中止。
func (s *StreamSender) Halted() bool { return s.halted }

// HaltErr 返回中止原因；未中止时为 nil。
func (s *StreamSender) HaltErr() error { return s.haltErr }

func (s *StreamSender) send(ctx context.Context, text string, first bool) {
	if first && !s.hinted && s.opts.TypingHint != "" {
		s.hinted = true
		if _, err := s.sender.Send(ctx, s.target, event.Message{event.Text(s.opts.TypingHint)}); err != nil {
			s.halt("typing hint send failed: " + err.Error())
			return
		}
	}
	if _, err := s.sender.Send(ctx, s.target, event.Message{event.Text(text)}); err != nil {
		s.halt("incremental send failed: " + err.Error())
		return
	}
	s.sends++
}

func (s *StreamSender) replace(ctx context.Context, full string) {
	if !s.opts.EditMessages || s.opts.Edit == nil {
		// 追加式平台无法撤回已发送内容：中止，绝不重复发送导致错乱。
		s.halt("platform cannot edit messages")
		return
	}
	if err := s.opts.Edit(ctx, s.target, full); err != nil {
		s.halt("edit message failed: " + err.Error())
	}
}

func (s *StreamSender) halt(reason string) {
	if s.halted {
		return
	}
	s.halted = true
	s.haltErr = fmt.Errorf("%w: %s", ErrStreamHalted, reason)
	if s.opts.OnHalt != nil {
		s.opts.OnHalt(reason)
	}
}
