package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/retry"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func userReq() *ChatRequest {
	return &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
}

func Test_F26_FakeLLMScriptsResponsesAndRecordsCalls(t *testing.T) {
	t.Parallel()
	f := NewFakeLLM(
		ScriptedResponse{Response: &ChatResponse{Content: "first", FinishReason: "stop"}},
		ScriptedResponse{Response: &ChatResponse{Content: "second", FinishReason: "stop"}},
	)
	ctx := context.Background()
	first, err := f.Chat(ctx, userReq())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	second, err := f.Chat(ctx, userReq())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if first.Content != "first" || second.Content != "second" {
		t.Fatalf("scripted responses: actual=(%q,%q)", first.Content, second.Content)
	}
	if f.Calls() != 2 {
		t.Fatalf("calls: actual=%d expected=2", f.Calls())
	}
	if len(f.Requests()) != 2 || f.LastRequest().Messages[0].Content != "hi" {
		t.Fatalf("request recording: %+v", f.Requests())
	}
}

func Test_F26_EmptyMessagesReturnError(t *testing.T) {
	t.Parallel()
	f := NewFakeLLM()
	if _, err := f.Chat(context.Background(), &ChatRequest{}); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("Chat with no messages: actual=%v expected=ErrNoMessages", err)
	}
	if _, err := f.ChatStream(context.Background(), &ChatRequest{}); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("ChatStream with no messages: actual=%v expected=ErrNoMessages", err)
	}
	var req *ChatRequest
	if err := req.Validate(); !errors.Is(err, ErrNoMessages) {
		t.Fatalf("nil request Validate: actual=%v expected=ErrNoMessages", err)
	}
}

func Test_F26_FakesSatisfyTheInterface(t *testing.T) {
	t.Parallel()
	var l LLM = NewFakeLLM()
	if _, ok := l.(*FakeLLM); !ok {
		t.Fatalf("LLM interface does not hold the fake")
	}
	var r LLM = NewRetryLLM(NewFakeLLM(), retry.Default())
	if _, ok := r.(*RetryLLM); !ok {
		t.Fatalf("LLM interface does not hold the retry decorator")
	}
}

func Test_F28_StreamProducerExitsWhenConsumerStops(t *testing.T) {
	t.Parallel()
	chunks := make([]Chunk, 0, 50)
	for i := 0; i < 50; i++ {
		chunks = append(chunks, Chunk{Content: "x"})
	}
	chunks = append(chunks, Chunk{Done: true, FinishReason: "stop"})
	f := NewFakeLLM(ScriptedResponse{Chunks: chunks, Delay: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.ChatStream(ctx, userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("stream closed too early at %d", i)
		}
	}
	start := time.Now()
	cancel() // 消费者提前退出

	closed := make(chan struct{})
	go func() {
		for range ch {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("producer did not exit within 200ms after cancel")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("producer exit too slow: %v", elapsed)
	}
}

func Test_F28_UpstreamErrorBecomesTerminalChunk(t *testing.T) {
	t.Parallel()
	boom := errors.New("upstream exploded")
	f := NewFakeLLM(ScriptedResponse{
		Chunks:    []Chunk{{Content: "partial"}, {Content: "more"}},
		StreamErr: boom,
	})
	ch, err := f.ChatStream(context.Background(), userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var last Chunk
	var count int
	for c := range ch {
		last = c
		count++
	}
	if count == 0 {
		t.Fatalf("no chunks received")
	}
	if !last.Done || !errors.Is(last.Err, boom) {
		t.Fatalf("terminal chunk: actual=%+v expected Done=true Err=%v", last, boom)
	}
}

func Test_F28_StreamHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	f := NewFakeLLM(ScriptedResponse{Chunks: []Chunk{{Content: "a"}, {Content: "b"}}, Delay: 10 * time.Millisecond})
	ch, err := f.ChatStream(ctx, userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	cancel()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // 生产者已退出
			}
		case <-deadline:
			t.Fatalf("stream did not terminate after ctx cancel")
		}
	}
}

func Test_F30_RetrySucceedsAfterRetryableFailures(t *testing.T) {
	t.Parallel()
	boom := &StatusError{StatusCode: 500, Body: "oops"}
	f := NewFakeLLM(
		ScriptedResponse{Err: boom},
		ScriptedResponse{Err: boom},
		ScriptedResponse{Response: &ChatResponse{Content: "ok", FinishReason: "stop"}},
	)
	r := NewRetryLLM(f, retry.Policy{
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	})
	resp, err := r.Chat(context.Background(), userReq())
	if err != nil {
		t.Fatalf("Chat: actual=%v expected=nil", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content: actual=%q expected=ok", resp.Content)
	}
	if f.Calls() != 3 {
		t.Fatalf("attempts: actual=%d expected=3", f.Calls())
	}
}

func Test_F30_NonRetryableStopsImmediately(t *testing.T) {
	t.Parallel()
	bad := &StatusError{StatusCode: 400, Body: "bad request"}
	f := NewFakeLLM(ScriptedResponse{Err: bad}, ScriptedResponse{Response: &ChatResponse{Content: "should not run"}})
	r := NewRetryLLM(f, retry.Policy{
		MaxAttempts: 5,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	})
	_, err := r.Chat(context.Background(), userReq())
	if !errors.Is(err, bad) {
		t.Fatalf("Chat: actual=%v expected=%v", err, bad)
	}
	if f.Calls() != 1 {
		t.Fatalf("attempts: actual=%d expected=1 (400 must not be retried)", f.Calls())
	}
}

func Test_F30_DefaultClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"500 retryable", &StatusError{StatusCode: 500}, true},
		{"429 retryable", &StatusError{StatusCode: 429}, true},
		{"400 not retryable", &StatusError{StatusCode: 400}, false},
		{"401 not retryable", &StatusError{StatusCode: 401}, false},
		{"403 not retryable", &StatusError{StatusCode: 403}, false},
		{"404 not retryable", &StatusError{StatusCode: 404}, false},
		{"deadline retryable", context.DeadlineExceeded, true},
		{"nil not retryable", nil, false},
	}
	for _, tc := range cases {
		if got := DefaultRetryable(tc.err); got != tc.want {
			t.Fatalf("%s: actual=%v expected=%v", tc.name, got, tc.want)
		}
	}
}

func Test_F30_ContextCancelDuringBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := NewFakeLLM(ScriptedResponse{Err: &StatusError{StatusCode: 503}})
	r := NewRetryLLM(f, retry.Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour})
	start := time.Now()
	_, err := r.Chat(ctx, userReq())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat: actual=%v expected=context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation was not honoured: %v", elapsed)
	}
}

func Test_F30_StreamRetriesOnlyBeforeFirstChunk(t *testing.T) {
	t.Parallel()
	boom := &StatusError{StatusCode: 502}
	f := NewFakeLLM(
		ScriptedResponse{Err: boom},
		ScriptedResponse{Chunks: []Chunk{{Content: "ok"}, {Done: true}}},
	)
	r := NewRetryLLM(f, retry.Policy{
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	})
	ch, err := r.ChatStream(context.Background(), userReq())
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var got string
	for c := range ch {
		got += c.Content
	}
	if got != "ok" {
		t.Fatalf("streamed content: actual=%q expected=ok", got)
	}
	if f.Streams() != 2 {
		t.Fatalf("stream attempts: actual=%d expected=2", f.Streams())
	}
}
