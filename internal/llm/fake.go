package llm

import (
	"context"
	"sync"
	"time"
)

// ScriptedResponse 是 FakeLLM 的一次预设应答。
type ScriptedResponse struct {
	Response *ChatResponse
	Chunks   []Chunk
	Err      error
	Delay    time.Duration
	// StreamErr 在流式输出若干分片后注入错误。
	StreamErr error
}

// FakeLLM 是脚本化的 LLM 实现（F-76：手写 fake，不引 mock 框架）。
type FakeLLM struct {
	mu         sync.Mutex
	script     []ScriptedResponse
	calls      int
	streams    int
	requests   []*ChatRequest
	blockUntil chan struct{}
}

// NewFakeLLM 用脚本构造。
func NewFakeLLM(script ...ScriptedResponse) *FakeLLM {
	return &FakeLLM{script: script}
}

// Calls 返回 Chat 被调用的次数。
func (f *FakeLLM) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Streams 返回 ChatStream 被调用的次数。
func (f *FakeLLM) Streams() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams
}

// LastRequest 返回最后一次请求（副本指针）。
func (f *FakeLLM) LastRequest() *ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// Requests 返回全部请求记录。
func (f *FakeLLM) Requests() []*ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*ChatRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// BlockNext 让下一次调用阻塞，直到返回的函数被调用（测试并发用）。
func (f *FakeLLM) BlockNext() func() {
	ch := make(chan struct{})
	f.mu.Lock()
	f.blockUntil = ch
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func (f *FakeLLM) next(req *ChatRequest) (ScriptedResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	idx := f.calls
	f.calls++
	block := f.blockUntil
	f.mu.Unlock()

	if block != nil {
		<-block
	}
	if err := req.Validate(); err != nil {
		return ScriptedResponse{}, err
	}
	if idx >= len(f.script) {
		return ScriptedResponse{Response: &ChatResponse{Content: "ok", FinishReason: "stop"}}, nil
	}
	return f.script[idx], nil
}

// Chat 返回脚本中的下一次非流式应答。
func (f *FakeLLM) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	sc, err := f.next(req)
	if err != nil {
		return nil, err
	}
	if sc.Delay > 0 {
		select {
		case <-time.After(sc.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if sc.Err != nil {
		return nil, sc.Err
	}
	if sc.Response == nil {
		return &ChatResponse{Content: "ok", FinishReason: "stop"}, nil
	}
	return sc.Response, nil
}

// ChatStream 按脚本产生分片；ctx 取消时立即停止。
func (f *FakeLLM) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	f.mu.Lock()
	f.streams++
	f.mu.Unlock()

	sc, err := f.next(req)
	if err != nil {
		return nil, err
	}
	if sc.Err != nil {
		return nil, sc.Err
	}

	ch := make(chan Chunk, StreamBuffer)
	go func() {
		defer close(ch)
		if len(sc.Chunks) == 0 {
			sc.Chunks = []Chunk{{Content: "ok"}, {Done: true, FinishReason: "stop"}}
		}
		delay := sc.Delay
		if delay <= 0 {
			delay = time.Millisecond
		}
		for i, c := range sc.Chunks {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if sc.StreamErr != nil && i == len(sc.Chunks)-1 {
				c.Err = sc.StreamErr
				c.Done = true
			}
			if !SendChunk(ctx, ch, c) {
				return
			}
		}
	}()
	return ch, nil
}

var _ LLM = (*FakeLLM)(nil)
