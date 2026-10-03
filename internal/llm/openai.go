package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultMaxBytes 是单次上游响应体的读取上限。
const DefaultMaxBytes = 8 << 20

// OpenAIConfig 描述一个 OpenAI 兼容端点。
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	// Client 由组合根注入（应当来自 httpx，带超时与 SSRF 防护）。
	Client *http.Client
	// MaxBytes 为 0 时使用 DefaultMaxBytes。
	MaxBytes int64
	// Thinking 控制思考模式开关（DeepSeek 的 {"thinking":{"type":...}}）。
	// nil 表示不下发该字段，由服务端使用默认值。
	Thinking *bool
	// ReasoningEffort 是思考强度：low / high / max。空串表示不下发。
	ReasoningEffort string
	// IncludeStreamUsage 在流式请求里带上 stream_options.include_usage，
	// 否则拿不到 usage（也就没有缓存命中计量）。
	IncludeStreamUsage bool
	// OnWarn 接收流式聚合中的异常告警（如同一 index 的 tool_call name 重复）；可为 nil。
	OnWarn func(string)
	// DisableJSONSchema 声明该端点不支持 response_format.type=json_schema（F-31）。
	// 置位时自动降级为 json_object，并把 schema 文本附到消息末尾。
	DisableJSONSchema bool
}

// OpenAI 是 OpenAI 兼容实现。
type OpenAI struct {
	cfg OpenAIConfig
}

// NewOpenAI 构造实现。
func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	return &OpenAI{cfg: cfg}
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Index    *int         `json:"index,omitempty"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Name             string         `json:"name,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

// wireThinking 是思考模式开关（DeepSeek OpenAI 格式）。
type wireThinking struct {
	Type string `json:"type"`
}

// wireStreamOptions 控制流式附加行为。
type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type wireResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

type wireRequest struct {
	Model           string              `json:"model"`
	Messages        []wireMessage       `json:"messages"`
	Tools           []wireTool          `json:"tools,omitempty"`
	Temperature     float64             `json:"temperature,omitempty"`
	MaxTokens       int                 `json:"max_tokens,omitempty"`
	ResponseFormat  *wireResponseFormat `json:"response_format,omitempty"`
	Stream          bool                `json:"stream,omitempty"`
	StreamOptions   *wireStreamOptions  `json:"stream_options,omitempty"`
	Thinking        *wireThinking       `json:"thinking,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		Delta        wireMessage `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage wireUsage `json:"usage"`
}

// wireUsage 是回包里的 token 计量。
//
// prompt_cache_hit_tokens / prompt_cache_miss_tokens 是 DeepSeek 前缀缓存的命中与未命中
// 输入 token（见 api-docs.deepseek.com/guides/kv_cache）。
type wireUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens   int `json:"prompt_cache_miss_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// toUsage 把线上计量转成内部计量。
func toUsage(u wireUsage) Usage {
	return Usage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		PromptCacheHitTokens:  u.PromptCacheHitTokens,
		PromptCacheMissTokens: u.PromptCacheMissTokens,
		ReasoningTokens:       u.CompletionTokensDetails.ReasoningTokens,
	}
}

// toWire 把请求转成线上格式。
//
// 关键：assistant 的 tool_calls 与 tool 的 tool_call_id 必须原样带上，否则多轮工具
// 调用会在第二轮被服务端 400 拒绝（反模式 #14）。
func (o *OpenAI) toWire(req *ChatRequest, stream bool) wireRequest {
	out := wireRequest{Model: o.cfg.Model, Stream: stream}
	if o.cfg.Thinking != nil {
		t := "disabled"
		if *o.cfg.Thinking {
			t = "enabled"
		}
		out.Thinking = &wireThinking{Type: t}
	}
	if o.cfg.ReasoningEffort != "" {
		out.ReasoningEffort = o.cfg.ReasoningEffort
	}
	if stream && o.cfg.IncludeStreamUsage {
		out.StreamOptions = &wireStreamOptions{IncludeUsage: true}
	}
	if req != nil {
		// 思考模式不支持 temperature/presence_penalty/frequency_penalty——传了不报错也不生效。
		// 既然如此就不下发，免得产生"设了却没效果"的错觉。
		thinkingOn := o.cfg.Thinking != nil && *o.cfg.Thinking
		if req.Temperature > 0 && !thinkingOn {
			out.Temperature = req.Temperature
		}
		out.MaxTokens = req.MaxTokens
		var schemaText string
		if req.ResponseFormat != nil {
			// F-31：端点不支持 json_schema 时降级为 json_object，并附 schema 文本。
			format, text := AdaptResponseFormat(*req.ResponseFormat, !o.cfg.DisableJSONSchema)
			out.ResponseFormat = &wireResponseFormat{Type: format.Type}
			if format.Type == TypeJSONSchema && len(format.Schema) > 0 {
				out.ResponseFormat.JSONSchema = format.Schema
			}
			schemaText = text
		}
		for _, m := range req.Messages {
			wm := wireMessage{
				Role:             string(m.Role),
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				Name:             m.Name,
				ToolCallID:       m.ToolCallID,
			}
			for _, tc := range m.ToolCalls {
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: wireFunction{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
			out.Messages = append(out.Messages, wm)
		}
		for _, t := range req.Tools {
			var wt wireTool
			wt.Type = "function"
			wt.Function.Name = t.Name
			wt.Function.Description = t.Description
			wt.Function.Parameters = t.Parameters
			out.Tools = append(out.Tools, wt)
		}
		if schemaText != "" {
			// 附在消息末尾而不是改写开头 system：ADR-0002 的稳定前缀不被打断。
			out.Messages = append(out.Messages, wireMessage{Role: string(RoleSystem), Content: schemaText})
		}
	}
	return out
}

func (o *OpenAI) endpoint() string {
	base := strings.TrimSuffix(o.cfg.BaseURL, "/")
	return base + "/chat/completions"
}

func (o *OpenAI) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build llm request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
	return httpReq, nil
}

// Chat 发起非流式请求。
func (o *OpenAI) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(o.toWire(req, false))
	if err != nil {
		return nil, fmt.Errorf("encode llm request: %w", err)
	}
	httpReq, err := o.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := o.cfg.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, o.cfg.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read llm response: %w", err)
	}
	if int64(len(raw)) > o.cfg.MaxBytes {
		return nil, fmt.Errorf("llm response exceeds %d bytes", o.cfg.MaxBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: truncate(string(raw), 512)}
	}

	var parsed wireResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("llm response has no choices")
	}
	choice := parsed.Choices[0]
	out := &ChatResponse{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		FinishReason:     choice.FinishReason,
		Usage:            toUsage(parsed.Usage),
	}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}

// ChatStream 发起流式请求。
//
// 生产者负责 close；所有发送都经 SendChunk（监听 ctx）；上游 EOF 与错误都会转成
// 一个终止分片后关闭 channel（F-28）。
func (o *OpenAI) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(o.toWire(req, true))
	if err != nil {
		return nil, fmt.Errorf("encode llm request: %w", err)
	}
	httpReq, err := o.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	//nolint:bodyclose // resp.Body 由下面的 goroutine 负责关闭（pumpSSE 之后）
	resp, err := o.cfg.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm stream request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: truncate(string(raw), 512)}
	}

	ch := make(chan Chunk, StreamBuffer)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		o.pumpSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

func (o *OpenAI) pumpSSE(ctx context.Context, body io.Reader, ch chan<- Chunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), o.cfg.MaxBytes64())
	finish := ""
	var usage *Usage
	agg := NewToolCallAggregator()
	agg.OnWarn = o.cfg.OnWarn
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			// 终止分片携带 usage 与聚合后的完整工具调用（F-29）。
			SendChunk(ctx, ch, Chunk{Done: true, FinishReason: finish, ToolCalls: agg.Finalize(), Usage: usage})
			return
		}
		var parsed wireResponse
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			// 单个分片解析失败不应中断整条流。
			continue
		}
		if parsed.Usage.TotalTokens > 0 || parsed.Usage.PromptTokens > 0 {
			// 用量分片不产生内容，但要留下缓存命中计量。
			u := toUsage(parsed.Usage)
			usage = &u
			continue
		}
		for _, choice := range parsed.Choices {
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			chunk := Chunk{
				Content:      choice.Delta.Content,
				Reasoning:    choice.Delta.ReasoningContent,
				FinishReason: choice.FinishReason,
			}
			// F-29：分片只聚合、不逐片上报，避免消费者执行半截 arguments；
			// 完整调用在终止分片上一次性给出。
			for i, tc := range choice.Delta.ToolCalls {
				index := i
				if tc.Index != nil {
					index = *tc.Index
				}
				agg.Add(ToolCallDelta{
					Index:     index,
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
			if chunk.Content == "" && chunk.Reasoning == "" && chunk.FinishReason == "" {
				continue
			}
			if !SendChunk(ctx, ch, chunk) {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// 中途出错：丢弃已聚合的调用，避免执行半截参数（F-29 边界：默认丢弃）。
		SendChunk(ctx, ch, Chunk{Done: true, Err: fmt.Errorf("read llm stream: %w", err)})
		return
	}
	// 终止分片携带 usage 与聚合后的完整工具调用（F-29）。
	SendChunk(ctx, ch, Chunk{Done: true, FinishReason: finish, ToolCalls: agg.Finalize(), Usage: usage})
}

// MaxBytes64 返回扫描缓冲上限。
func (c OpenAIConfig) MaxBytes64() int { return int(c.MaxBytes) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var _ LLM = (*OpenAI)(nil)
