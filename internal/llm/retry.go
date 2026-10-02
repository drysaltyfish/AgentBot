package llm

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/drysaltyfish/agentbot/internal/retry"
)

// StatusError 携带上游 HTTP 状态码，供 Retryable 判定。
type StatusError struct {
	StatusCode int
	Body       string
}

// Error 实现 error。
func (e *StatusError) Error() string {
	return fmt.Sprintf("llm upstream returned status %d: %s", e.StatusCode, e.Body)
}

// DefaultRetryable 实现 F-30 的默认分类：
// 网络错误、超时、429、5xx 可重试；400/401/403/404 不可重试（避免重复计费）。
func DefaultRetryable(err error) bool {
	if err == nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		if se.StatusCode == 429 || se.StatusCode >= 500 {
			return true
		}
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// RetryLLM 是 LLM 的重试装饰器。
type RetryLLM struct {
	next   LLM
	policy retry.Policy
}

// NewRetryLLM 构造装饰器；未指定 Retryable 时使用 DefaultRetryable。
func NewRetryLLM(next LLM, p retry.Policy) *RetryLLM {
	if p.Retryable == nil {
		p.Retryable = DefaultRetryable
	}
	return &RetryLLM{next: next, policy: p}
}

// Chat 按策略重试。
func (r *RetryLLM) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return retry.Do(ctx, r.policy, func(ctx context.Context, attempt int) (*ChatResponse, error) {
		return r.next.Chat(ctx, req)
	})
}

// ChatStream 只在"开流之前"失败才重试：一旦拿到 channel 就不再重试，避免重复内容。
func (r *RetryLLM) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	ch, err := retry.Do(ctx, r.policy, func(ctx context.Context, attempt int) (<-chan Chunk, error) {
		return r.next.ChatStream(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

var _ LLM = (*RetryLLM)(nil)
