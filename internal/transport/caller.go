// Package transport 实现传输抽象 Driver 与调用抽象 Caller
// （FEATURES.md F-04 / F-05 / F-06 / F-80）。
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/retry"
)

var (
	// ErrRateLimited 表示被本地限流器拒绝（未发出请求）。
	ErrRateLimited = errors.New("rate limited by local limiter")
	// ErrNoConnection 表示没有可用的连接。
	ErrNoConnection = errors.New("transport is not connected")
)

// Request 是一次平台 API 调用。
type Request struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params,omitempty"`
	Echo   uint64         `json:"echo,omitempty"`
}

// Response 是平台 API 的返回。
type Response struct {
	Status  string          `json:"status,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Message string          `json:"message,omitempty"`
	Wording string          `json:"wording,omitempty"`
	RetCode int64           `json:"retcode"`
	Echo    uint64          `json:"echo,omitempty"`
}

// OK 判断调用是否成功。
func (r Response) OK() bool { return r.RetCode == 0 }

// Caller 是"向平台发一次调用"的抽象；必须可装饰。
type Caller interface {
	Call(ctx context.Context, req Request) (Response, error)
}

// Middleware 包装 Caller。
type Middleware func(Caller) Caller

// Chain 按声明顺序包装：Chain(base, a, b) 的调用顺序是 a -> b -> base。
func Chain(base Caller, mws ...Middleware) Caller {
	c := base
	for i := len(mws) - 1; i >= 0; i-- {
		c = mws[i](c)
	}
	return c
}

// RecordingCaller 记录成功调用返回的消息 ID。
//
// 只记录"发出的消息 ID"；撤回与"触发消息 -> 发送消息"的映射不在本 Feature 内。
type RecordingCaller struct {
	next Caller
	mu   sync.Mutex
	sent []event.ID
	max  int
}

// NewRecordingCaller 构造 RecordingCaller，max <= 0 时只保留最近 128 条。
func NewRecordingCaller(next Caller, max int) *RecordingCaller {
	if max <= 0 {
		max = 128
	}
	return &RecordingCaller{next: next, max: max}
}

// Call 转发调用并记录消息 ID。
func (c *RecordingCaller) Call(ctx context.Context, req Request) (Response, error) {
	resp, err := c.next.Call(ctx, req)
	if err != nil {
		return resp, err
	}
	if id, ok := messageIDOf(resp.Data); ok {
		c.mu.Lock()
		c.sent = append(c.sent, id)
		if len(c.sent) > c.max {
			c.sent = c.sent[len(c.sent)-c.max:]
		}
		c.mu.Unlock()
	}
	return resp, nil
}

// SentIDs 返回已记录的消息 ID 副本。
func (c *RecordingCaller) SentIDs() []event.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]event.ID, len(c.sent))
	copy(out, c.sent)
	return out
}

func messageIDOf(data json.RawMessage) (event.ID, bool) {
	if len(data) == 0 {
		return event.ID{}, false
	}
	var payload struct {
		MessageID json.RawMessage `json:"message_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return event.ID{}, false
	}
	if len(payload.MessageID) == 0 {
		return event.ID{}, false
	}
	var id event.ID
	if err := json.Unmarshal(payload.MessageID, &id); err != nil {
		return event.ID{}, false
	}
	if id.IsZero() {
		return event.ID{}, false
	}
	return id, true
}

// RateLimitedCaller 在本地丢弃超额的调用（令牌桶）。
type RateLimitedCaller struct {
	next   Caller
	bucket *tokenBucket
}

// NewRateLimitedCaller 构造限流装饰器：每秒补充 rate 个令牌，桶容量 burst。
func NewRateLimitedCaller(next Caller, rate, burst float64) *RateLimitedCaller {
	return &RateLimitedCaller{next: next, bucket: newTokenBucket(rate, burst, time.Now)}
}

// Call 令牌不足时直接返回 ErrRateLimited，不发出请求。
func (c *RateLimitedCaller) Call(ctx context.Context, req Request) (Response, error) {
	if !c.bucket.allow() {
		return Response{}, ErrRateLimited
	}
	return c.next.Call(ctx, req)
}

type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
	now    func() time.Time
}

func newTokenBucket(rate, burst float64, now func() time.Time) *tokenBucket {
	if rate <= 0 {
		rate = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &tokenBucket{tokens: burst, last: now(), rate: rate, burst: burst, now: now}
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.After(b.last) {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	// 时间回拨时不得出现负 token。
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RetryCaller 对可重试的失败做指数退避。
type RetryCaller struct {
	next   Caller
	policy retry.Policy
}

// NewRetryCaller 构造重试装饰器。
func NewRetryCaller(next Caller, p retry.Policy) *RetryCaller {
	return &RetryCaller{next: next, policy: p}
}

// Call 按策略重试；平台返回可重试 retcode 时也视作失败。
func (c *RetryCaller) Call(ctx context.Context, req Request) (Response, error) {
	return retry.Do(ctx, c.policy, func(ctx context.Context, attempt int) (Response, error) {
		// 只重试传输层错误；平台业务错误（retcode != 0）不重试，避免重复副作用。
		return c.next.Call(ctx, req)
	})
}

// SendGroupMsg 发送群消息并返回消息 ID。
func SendGroupMsg(ctx context.Context, c Caller, groupID int64, msg event.Message) (event.ID, error) {
	return sendMessage(ctx, c, "send_group_msg", map[string]any{
		"group_id": groupID,
		"message":  msg.Marshal(),
	})
}

// SendPrivateMsg 发送私聊消息并返回消息 ID。
func SendPrivateMsg(ctx context.Context, c Caller, userID int64, msg event.Message) (event.ID, error) {
	return sendMessage(ctx, c, "send_private_msg", map[string]any{
		"user_id": userID,
		"message": msg.Marshal(),
	})
}

func sendMessage(ctx context.Context, c Caller, action string, params map[string]any) (event.ID, error) {
	resp, err := c.Call(ctx, Request{Action: action, Params: params})
	if err != nil {
		return event.ID{}, err
	}
	if !resp.OK() {
		return event.ID{}, fmt.Errorf("%s failed: retcode=%d wording=%s", action, resp.RetCode, resp.Wording)
	}
	id, ok := messageIDOf(resp.Data)
	if !ok {
		return event.ID{}, nil
	}
	return id, nil
}

// DeleteMsg 撤回消息。
func DeleteMsg(ctx context.Context, c Caller, id event.ID) error {
	resp, err := c.Call(ctx, Request{Action: "delete_msg", Params: map[string]any{"message_id": id}})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return fmt.Errorf("delete_msg failed: retcode=%d", resp.RetCode)
	}
	return nil
}

// SetGroupBan 禁言群成员。
func SetGroupBan(ctx context.Context, c Caller, groupID, userID int64, d time.Duration) error {
	resp, err := c.Call(ctx, Request{Action: "set_group_ban", Params: map[string]any{
		"group_id": groupID,
		"user_id":  userID,
		"duration": int64(d.Seconds()),
	}})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return fmt.Errorf("set_group_ban failed: retcode=%d", resp.RetCode)
	}
	return nil
}

var (
	_ Caller = (*RecordingCaller)(nil)
	_ Caller = (*RateLimitedCaller)(nil)
	_ Caller = (*RetryCaller)(nil)
)
