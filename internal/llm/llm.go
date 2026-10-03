// Package llm 实现统一 LLM 接口、流式契约与重试（FEATURES.md F-26 / F-28 / F-30）。
//
// 所有上层结构体字段必须是 LLM 接口类型，禁止出现具体实现类型。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrNoMessages 表示请求没有任何消息。
	ErrNoMessages = errors.New("chat request has no messages")
	// ErrNotImplemented 表示该实现不支持此能力。
	ErrNotImplemented = errors.New("not implemented")
)

// Role 是消息角色。
type Role string

// 角色常量。
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是一次工具调用。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message 是发给模型的一条消息。
type Message struct {
	Role    Role
	Content string
	// ReasoningContent 是思考模式的思维链。
	//
	// DeepSeek 的规定：请求**携带 tools** 时，历史轮次的 reasoning_content 必须回传，
	// 且会被拼进上下文；不携带 tools 时回传也会被忽略。这里一律透传，由服务端决定。
	ReasoningContent string
	Name             string
	ToolCalls        []ToolCall
	ToolCallID       string
	// Pinned 标记该消息在 F-32 的上下文裁剪中**永不被裁掉**。
	//
	// system 提示词契约（ADR-0002 的稳定前缀）应置为 true；
	// 零值 false 表示可按需裁剪，因此存量构造不受影响。
	Pinned bool
}

// ToolSpec 是导出给模型的工具 schema。
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ResponseFormat 控制结构化输出（F-31 的基础）。
type ResponseFormat struct {
	Type   string
	Schema json.RawMessage
	Strict bool
}

// Usage 是 token 计量。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// PromptCacheHitTokens / PromptCacheMissTokens 是 DeepSeek 前缀缓存的命中与未命中
	// 输入 token 数（见 api-docs.deepseek.com/guides/kv_cache）。其它供应商不返回时为 0。
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
	// ReasoningTokens 是思考模式下思维链占用的 token（供应商未提供时为 0）。
	ReasoningTokens int
}

// CacheHitRatio 返回输入侧的前缀缓存命中率（0~1）。
//
// 这是"缓存优先"设计的唯一客观指标：命中率低说明前缀被改写或注入了易变内容。
func (u Usage) CacheHitRatio() float64 {
	total := u.PromptCacheHitTokens + u.PromptCacheMissTokens
	if total <= 0 {
		return 0
	}
	return float64(u.PromptCacheHitTokens) / float64(total)
}

// ChatRequest 是一次对话请求。
type ChatRequest struct {
	Messages       []Message
	Tools          []ToolSpec
	Temperature    float64
	MaxTokens      int
	ResponseFormat *ResponseFormat
	Metadata       map[string]string
}

// Validate 做最小合法性检查。
func (r *ChatRequest) Validate() error {
	if r == nil || len(r.Messages) == 0 {
		return ErrNoMessages
	}
	return nil
}

// ChatResponse 是一次对话结果。
type ChatResponse struct {
	Content string
	// ReasoningContent 是思考模式的思维链。无 tools 时无需回传，也不应写入历史
	// （回传也会被服务端忽略），否则只是白白拉长上下文、拉低缓存命中。
	ReasoningContent string
	ToolCalls        []ToolCall
	FinishReason     string
	Usage            Usage
}

// Chunk 是流式分片；错误也走 channel（F-28）。
type Chunk struct {
	Content string
	// Reasoning 是思考模式的思维链增量。
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Done         bool
	Err          error
	// Usage 仅在终止分片上可能非空（需要 stream_options.include_usage）。
	Usage *Usage
}

// LLM 是统一的对话接口。
type LLM interface {
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error)
}

// StreamBuffer 是建议的流式 channel 缓冲大小。
const StreamBuffer = 16

// SendChunk 是所有流式生产者必须使用的发送函数：它监听 ctx，绝不阻塞泄漏。
//
// 返回 false 表示 ctx 已取消或 channel 已关闭，生产者应立即退出。
func SendChunk(ctx context.Context, ch chan<- Chunk, c Chunk) bool {
	select {
	case ch <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// DrainChunks 读取整个流并把分片（含最后的错误分片）交给 fn。
func DrainChunks(ch <-chan Chunk, fn func(Chunk)) {
	for c := range ch {
		if fn != nil {
			fn(c)
		}
	}
}

// FinishReasonToolCalls 是"模型要求调用工具"的终止原因。
const FinishReasonToolCalls = "tool_calls"

// Describe 返回便于日志的简短描述（不含消息全文）。
func (r *ChatRequest) Describe() string {
	if r == nil {
		return "chat_request<nil>"
	}
	return fmt.Sprintf("chat_request{messages=%d,tools=%d,max_tokens=%d}", len(r.Messages), len(r.Tools), r.MaxTokens)
}
