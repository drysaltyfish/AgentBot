// Package outbound 实现统一出口过滤链（FEATURES.md F-55）。
//
// 所有对外发送必须经过 Sender.Send 这一个出口；禁止任何旁路直接调用底层 Caller。
package outbound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

var (
	// ErrEmptyAfterFilter 表示过滤后没有内容，不应发送。
	ErrEmptyAfterFilter = errors.New("message is empty after outbound filtering")
	// ErrNoTarget 表示既没有群号也没有用户号。
	ErrNoTarget = errors.New("outbound target is empty")
	// ErrFilterPanic 表示某个 filter panic 过（已放行原始内容）。
	ErrFilterPanic = errors.New("outbound filter panicked")
)

// 固定处理链的顺序（F-55）。名称同时用于单独关闭某个 filter。
const (
	FilterLength      = "length_limit"
	FilterSensitive   = "sensitive_replace"
	FilterDenoise     = "denoise"
	FilterTextReplace = "text_replace_out"
	FilterTrimTail    = "trim_tail"
	FilterNormalize   = "normalize"
)

// Order 是处理链的固定顺序。
var Order = []string{FilterLength, FilterSensitive, FilterDenoise, FilterTextReplace, FilterTrimTail, FilterNormalize}

// Filter 是处理链的一环；必须幂等。
type Filter func(string) string

// AuditRecord 是一条出口审计记录。
type AuditRecord struct {
	At       time.Time
	GroupID  int64
	UserID   int64
	Original string
	Filtered string
	Dropped  bool
	Reason   string
	Message  event.ID
	Error    string
}

// Option 配置 Chain。
type Option func(*Chain)

// WithFilter 注册或替换某个位置的 filter。
func WithFilter(name string, f Filter) Option {
	return func(c *Chain) {
		c.filters[name] = f
		c.userSet[name] = true
	}
}

// WithMaxLength 设置单条消息的长度上限（默认 2000）。
func WithMaxLength(n int) Option {
	return func(c *Chain) {
		if n > 0 {
			c.maxLen = n
		}
	}
}

// WithPanicHook 注入 filter panic 的回调。
func WithPanicHook(fn func(name string, recovered any)) Option {
	return func(c *Chain) { c.onPanic = fn }
}

// Chain 是出口处理链。
type Chain struct {
	mu       sync.RWMutex
	filters  map[string]Filter
	disabled map[string]bool
	userSet  map[string]bool
	maxLen   int
	onPanic  func(name string, recovered any)
}

// New 构造处理链，并装上默认实现。
func New(opts ...Option) *Chain {
	c := &Chain{
		filters:  map[string]Filter{},
		disabled: map[string]bool{},
		userSet:  map[string]bool{},
		maxLen:   2000,
	}
	identity := func(s string) string { return s }
	c.filters[FilterLength] = LengthLimit(c.maxLen)
	c.filters[FilterSensitive] = identity
	c.filters[FilterDenoise] = Denoise()
	c.filters[FilterTextReplace] = ReplaceText(nil)
	c.filters[FilterTrimTail] = TrimTail()
	c.filters[FilterNormalize] = identity
	for _, o := range opts {
		o(c)
	}
	// 只有调用方没有自带 length filter 时，才用 maxLen 重建默认实现。
	if !c.userSet[FilterLength] {
		c.filters[FilterLength] = LengthLimit(c.maxLen)
	}
	return c
}

// Enable 打开/关闭某个 filter（便于定位问题）。
func (c *Chain) Enable(name string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled[name] = !on
}

// Apply 依次执行处理链。任一 filter panic 时放行"进入该 filter 之前"的内容并告警。
func (c *Chain) Apply(s string) string {
	out := s
	for _, name := range Order {
		c.mu.RLock()
		f, ok := c.filters[name]
		off := c.disabled[name]
		c.mu.RUnlock()
		if !ok || off || f == nil {
			continue
		}
		out = c.run(name, f, out)
	}
	return out
}

func (c *Chain) run(name string, f Filter, in string) (out string) {
	out = in
	defer func() {
		if r := recover(); r != nil {
			out = in // 可用性优先：放行原始内容
			if c.onPanic != nil {
				c.onPanic(name, r)
			}
		}
	}()
	return f(in)
}

// LengthLimit 截断超长文本并标注。
func LengthLimit(max int) Filter {
	return func(s string) string {
		if max <= 0 {
			return s
		}
		runes := []rune(s)
		if len(runes) <= max {
			return s
		}
		const marker = "…[已截断]"
		keep := max - len([]rune(marker))
		if keep < 0 {
			keep = 0
		}
		return string(runes[:keep]) + marker
	}
}

// Denoise 折叠多余空白与非必要的连续空行（幂等）。
func Denoise() Filter {
	return func(s string) string {
		s = strings.ReplaceAll(s, "\n", "\n")
		lines := strings.Split(s, "\n")
		out := make([]string, 0, len(lines))
		blank := 0
		for _, line := range lines {
			trimmedRight := strings.TrimRight(line, " 	")
			if strings.TrimSpace(trimmedRight) == "" {
				blank++
				if blank > 1 {
					continue
				}
				out = append(out, "")
				continue
			}
			blank = 0
			out = append(out, trimmedRight)
		}
		return strings.Join(out, "\n")
	}
}

// TrimTail 去掉结尾的空白与换行（幂等）。
func TrimTail() Filter {
	return func(s string) string { return strings.TrimRight(s, " 	\n") }
}

// ReplaceText 按表替换文本（对应 ReplaceTextOut 环节）。
func ReplaceText(mapping map[string]string) Filter {
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	// 固定顺序，保证幂等与可复现。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return func(s string) string {
		for _, k := range keys {
			if k == "" {
				continue
			}
			s = strings.ReplaceAll(s, k, mapping[k])
		}
		return s
	}
}

// Target 是发送目标；群号与用户号二选一。
type Target struct {
	GroupID int64
	UserID  int64
	IsGroup bool
}

// GroupTarget 构造群目标。
func GroupTarget(groupID int64) Target { return Target{GroupID: groupID, IsGroup: true} }

// PrivateTarget 构造私聊目标。
func PrivateTarget(userID int64) Target { return Target{UserID: userID} }

// Sender 是唯一的对外发送出口。
type Sender struct {
	caller transport.Caller
	chain  *Chain
	audit  func(AuditRecord)
	now    func() time.Time
}

// SenderOption 配置 Sender。
type SenderOption func(*Sender)

// WithAudit 注入审计回调（F-60 在 M3 接入真实实现）。
func WithAudit(fn func(AuditRecord)) SenderOption {
	return func(s *Sender) { s.audit = fn }
}

// WithSenderClock 注入时间源。
func WithSenderClock(now func() time.Time) SenderOption {
	return func(s *Sender) {
		if now != nil {
			s.now = now
		}
	}
}

// NewSender 构造出口。
func NewSender(caller transport.Caller, chain *Chain, opts ...SenderOption) *Sender {
	if chain == nil {
		chain = New()
	}
	s := &Sender{caller: caller, chain: chain, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Chain 返回底层处理链。
func (s *Sender) Chain() *Chain { return s.chain }

// Send 过滤并发送一条消息。
//
// 过滤后无内容时不发送，但仍然写审计（F-55 边界）。
func (s *Sender) Send(ctx context.Context, target Target, msg event.Message) (event.ID, error) {
	if s.caller == nil {
		return event.ID{}, errors.New("outbound sender has no caller")
	}
	if !target.IsGroup && target.UserID == 0 {
		return event.ID{}, ErrNoTarget
	}

	filtered := s.filterMessage(msg)
	original := msg.PlainText()
	text := filtered.PlainText()
	rec := AuditRecord{At: s.now(), GroupID: target.GroupID, UserID: target.UserID, Original: original, Filtered: text}

	if len(filtered) == 0 || (text == "" && len(filtered) == 0) {
		rec.Dropped = true
		rec.Reason = "empty after filtering"
		s.record(rec)
		return event.ID{}, ErrEmptyAfterFilter
	}

	var (
		id  event.ID
		err error
	)
	if target.IsGroup {
		id, err = transport.SendGroupMsg(ctx, s.caller, target.GroupID, filtered)
	} else {
		id, err = transport.SendPrivateMsg(ctx, s.caller, target.UserID, filtered)
	}
	rec.Message = id
	if err != nil {
		rec.Error = err.Error()
	}
	s.record(rec)
	return id, err
}

func (s *Sender) filterMessage(msg event.Message) event.Message {
	out := make(event.Message, 0, len(msg))
	for _, seg := range msg {
		if seg.Type != event.TypeText {
			out = append(out, seg)
			continue
		}
		text := s.chain.Apply(seg.Data["text"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, event.Text(text))
	}
	// 过滤后可能只剩下空白文本段：整条消息视为空。
	if out.PlainText() == "" && len(msg) > 0 {
		onlyNonText := true
		for _, seg := range out {
			if seg.Type == event.TypeText {
				onlyNonText = false
				break
			}
		}
		if onlyNonText && len(out) == 0 {
			return event.Message{}
		}
	}
	return out
}

func (s *Sender) record(rec AuditRecord) {
	if s.audit != nil {
		s.audit(rec)
	}
}

var _ = fmt.Sprint
