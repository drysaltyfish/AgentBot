package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type pipeShared struct {
	closed chan struct{}
	once   sync.Once
}

type pipeEnd struct {
	out    chan []byte
	in     chan []byte
	shared *pipeShared
}

func newPipePair() (*pipeEnd, *pipeEnd) {
	a2b := make(chan []byte, 256)
	b2a := make(chan []byte, 256)
	s := &pipeShared{closed: make(chan struct{})}
	return &pipeEnd{out: a2b, in: b2a, shared: s}, &pipeEnd{out: b2a, in: a2b, shared: s}
}

func (p *pipeEnd) WriteMessage(ctx context.Context, data []byte) error {
	buf := append([]byte(nil), data...)
	select {
	case p.out <- buf:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.shared.closed:
		return io.ErrClosedPipe
	}
}

func (p *pipeEnd) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case d := <-p.in:
		return d, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.shared.closed:
		return nil, io.ErrClosedPipe
	}
}

func (p *pipeEnd) Close() error {
	p.shared.once.Do(func() { close(p.shared.closed) })
	return nil
}

func newTestClient(t *testing.T, endpoint *pipeEnd) *WSClient {
	t.Helper()
	return NewWSClient("ws://in-memory", nil, WithDialer(func(ctx context.Context, rawURL string) (wsConn, error) {
		return endpoint, nil
	}))
}

func Test_F06_ConcurrentCallsAreCorrelatedOutOfOrder(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: actual=%v expected=nil", err)
	}
	go func() { _ = c.Listen(ctx, func([]byte, Caller) {}) }()

	const n = 50
	go func() {
		reqs := make([]Request, 0, n)
		for i := 0; i < n; i++ {
			raw, err := serverEnd.ReadMessage(ctx)
			if err != nil {
				return
			}
			var rq Request
			if err := json.Unmarshal(raw, &rq); err != nil {
				return
			}
			reqs = append(reqs, rq)
		}
		// 故意倒序回包，验证是按 echo 配对而不是按到达顺序。
		for i := len(reqs) - 1; i >= 0; i-- {
			body, _ := json.Marshal(map[string]any{
				"status":  "ok",
				"retcode": 0,
				"echo":    reqs[i].Echo,
				"data":    map[string]any{"echo": reqs[i].Echo},
			})
			if err := serverEnd.WriteMessage(ctx, body); err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Call(ctx, Request{Action: "ping"})
			if err != nil {
				errs <- fmt.Errorf("Call: %w", err)
				return
			}
			var payload struct {
				Echo uint64 `json:"echo"`
			}
			if err := json.Unmarshal(resp.Data, &payload); err != nil {
				errs <- fmt.Errorf("decode data: %w", err)
				return
			}
			if payload.Echo != resp.Echo {
				errs <- fmt.Errorf("mismatched pairing: response echo=%d carried echo=%d", resp.Echo, payload.Echo)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := c.PendingCalls(); got != 0 {
		t.Fatalf("pending entries after all calls completed: actual=%d expected=0", got)
	}
}

func Test_F06_TimeoutLeavesNoPendingEntries(t *testing.T) {
	t.Parallel()
	clientEnd, _ := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	go func() { _ = c.Listen(ctx, func([]byte, Caller) {}) }()

	callCtx, callCancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer callCancel()
	if _, err := c.Call(callCtx, Request{Action: "never-answered"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call: actual=%v expected=context.DeadlineExceeded", err)
	}
	waitFor(t, "pending table drained", func() bool { return c.PendingCalls() == 0 })
}

func Test_F06_UnmatchedEchoIsDroppedWithoutPanic(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	go func() { _ = c.Listen(ctx, func([]byte, Caller) {}) }()

	if err := serverEnd.WriteMessage(ctx, []byte(`{"status":"ok","retcode":0,"echo":999999}`)); err != nil {
		t.Fatalf("write unmatched echo: %v", err)
	}

	// 读循环必须还活着：后续正常调用仍要成功。
	go func() {
		raw, err := serverEnd.ReadMessage(ctx)
		if err != nil {
			return
		}
		var rq Request
		_ = json.Unmarshal(raw, &rq)
		body, _ := json.Marshal(map[string]any{"status": "ok", "retcode": 0, "echo": rq.Echo})
		_ = serverEnd.WriteMessage(ctx, body)
	}()
	resp, err := c.Call(ctx, Request{Action: "after-unmatched"})
	if err != nil {
		t.Fatalf("Call after unmatched echo: actual=%v expected=nil", err)
	}
	if !resp.OK() {
		t.Fatalf("response: actual=%+v", resp)
	}
}

func Test_F06_HeartbeatsAreDroppedAndCounted(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	events := make(chan []byte, 8)
	go func() { _ = c.Listen(ctx, func(raw []byte, _ Caller) { events <- raw }) }()

	heartbeat := []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":1}`)
	message := []byte(`{"post_type":"message","message_type":"group","message_id":1}`)
	if err := serverEnd.WriteMessage(ctx, heartbeat); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	if err := serverEnd.WriteMessage(ctx, message); err != nil {
		t.Fatalf("write message: %v", err)
	}

	select {
	case got := <-events:
		if !strings.Contains(string(got), `"message"`) {
			t.Fatalf("heartbeat leaked to sink: actual=%s", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("real event was not delivered")
	}
	waitFor(t, "heartbeat counted", func() bool { return c.Heartbeats() == 1 })
}

func Test_F04_WSClientConnectFailureReturnsError(t *testing.T) {
	t.Parallel()
	c := NewWSClient("ws://nope", nil, WithDialer(func(ctx context.Context, rawURL string) (wsConn, error) {
		return nil, errors.New("connection refused")
	}))
	err := c.Connect(context.Background())
	if err == nil {
		t.Fatalf("Connect: actual=nil expected=error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error should carry the cause: actual=%v", err)
	}
	if err := c.Listen(context.Background(), nil); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Listen without connection: actual=%v expected=ErrNoConnection", err)
	}
}

func Test_F80_WSClientPassesAccessTokenToDialer(t *testing.T) {
	t.Parallel()
	var seen string
	c := NewWSClient("ws://host/path", NewAuth("tok-xyz", "", nil), WithDialer(func(ctx context.Context, rawURL string) (wsConn, error) {
		seen = rawURL
		endpoint, _ := newPipePair()
		return endpoint, nil
	}))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !strings.Contains(seen, "access_token=tok-xyz") {
		t.Fatalf("dial URL missing access_token: actual=%q", seen)
	}
}

func Test_F04_WSClientListenReturnsOnCancel(t *testing.T) {
	t.Parallel()
	clientEnd, _ := newPipePair()
	c := newTestClient(t, clientEnd)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Listen(ctx, func([]byte, Caller) {}) }()
	waitFor(t, "listen started", func() bool { return true })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Listen error: actual=%v expected=context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Listen did not return after ctx cancel")
	}
}
