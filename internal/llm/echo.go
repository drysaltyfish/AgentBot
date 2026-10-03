package llm

import (
	"context"
	"strings"
	"time"
)

// DefaultEchoPrefix 是假 LLM 的默认回复模板前缀。
const DefaultEchoPrefix = "你刚才跟我说："

// Echo 是联调用的假 LLM：不访问网络，把最后一条 user 消息按模板回显。
//
// 用途：在接入真实模型之前打通"事件 → 路由 → LLM → 回复"整条链路。
type Echo struct {
	// Prefix 是回复前缀；为空时使用 DefaultEchoPrefix。
	Prefix string
	// Delay 模拟模型耗时（默认 0）。
	Delay time.Duration
}

// NewEcho 构造假 LLM。
func NewEcho(prefix string) *Echo {
	if prefix == "" {
		prefix = DefaultEchoPrefix
	}
	return &Echo{Prefix: prefix}
}

// LastUserMessage 返回最后一条 user 消息的文本。
func LastUserMessage(req *ChatRequest) string {
	if req == nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

// Chat 直接按模板作答。
func (e *Echo) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if e.Delay > 0 {
		select {
		case <-time.After(e.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	prefix := e.Prefix
	if prefix == "" {
		prefix = DefaultEchoPrefix
	}
	content := prefix + strings.TrimSpace(LastUserMessage(req))
	return &ChatResponse{
		Content:      content,
		FinishReason: "stop",
		Usage:        Usage{PromptTokens: len(req.Messages), CompletionTokens: len([]rune(content)), TotalTokens: len(req.Messages) + len([]rune(content))},
	}, nil
}

// ChatStream 把整段回复切成一个内容分片 + 一个终止分片。
func (e *Echo) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	resp, err := e.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan Chunk, StreamBuffer)
	go func() {
		defer close(ch)
		if !SendChunk(ctx, ch, Chunk{Content: resp.Content}) {
			return
		}
		SendChunk(ctx, ch, Chunk{Done: true, FinishReason: resp.FinishReason})
	}()
	return ch, nil
}

var _ LLM = (*Echo)(nil)
