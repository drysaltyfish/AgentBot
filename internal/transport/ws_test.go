package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
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

// Test_F04_DisconnectAllowsReconnect 钉住重连所需的那一步。
//
// Connect 在 c.conn != nil 时直接返回 nil（幂等），而读循环因读错误返回时
// c.conn **不会**被清掉。所以想重连就必须先 Disconnect —— 否则 Connect 会
// 返回 nil 这个"成功"，客户端随后拿着一条已经死掉的连接继续用，
// 表现为"重连成功但永远收不到消息"。
//
// 同时钉住 Disconnect 与 Close 的区别：Close 是终态，之后 Connect 恒返回 ErrClosed。
func Test_F04_DisconnectAllowsReconnect(t *testing.T) {
	t.Parallel()
	dials := 0
	clientEnd, _ := newPipePair()
	c := NewWSClient("ws://in-memory", nil, WithDialer(func(context.Context, string) (wsConn, error) {
		dials++
		return clientEnd, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("首次 Connect: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("重复 Connect: %v", err)
	}
	if dials != 1 {
		t.Fatalf("已连接时 Connect 应幂等（不重新拨号），实际拨号 %d 次", dials)
	}

	// 断开前：Connect 是空操作，所以"重连"根本不会重新拨号——这正是坑。
	c.Disconnect()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Disconnect 之后 Connect: %v", err)
	}
	if dials != 2 {
		t.Fatalf("Disconnect 之后 Connect 必须重新拨号，实际拨号 %d 次", dials)
	}

	// Close 是终态：之后不允许再连。
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Connect(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 之后 Connect 应返回 ErrClosed，实际 %v", err)
	}
	if err := c.Connect(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 之后 Connect 应持续返回 ErrClosed，实际 %v", err)
	}
}

// Test_F04_ConnectIsNoopWhileStaleConnIsSet 把"必须先 Disconnect"这个前提写成断言。
//
// 这条断言的是**危险行为本身**：残留连接未清时，Connect 报告成功却不重新拨号。
// 它存在的意义是：一旦有人把 Connect 改成"按连接存活状态判断"，这条会失败，
// 提醒他重连路径的顺序依赖已经变了。
func Test_F04_ConnectIsNoopWhileStaleConnIsSet(t *testing.T) {
	t.Parallel()
	dials := 0
	clientEnd, _ := newPipePair()
	c := NewWSClient("ws://in-memory", nil, WithDialer(func(context.Context, string) (wsConn, error) {
		dials++
		return clientEnd, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// 模拟读循环因读错误返回：连接已经不可用，但 c.conn 仍指向它。
	go func() { _ = c.Listen(ctx, func([]byte, Caller) {}) }()
	_ = clientEnd.Close()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("残留连接存在时 Connect 返回 nil 是既有语义: %v", err)
	}
	if dials != 1 {
		t.Fatalf("残留连接存在时 Connect 不会重新拨号（这正是必须先 Disconnect 的原因），实际 %d 次", dials)
	}
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

// Test_F04_MessageEventsWithArrayMessageReachSink 是一条回归测试。
//
// 事件帧里的 message 是消息段数组，而 API 回包的 message 是字符串。早期把信封的
// message 字段声明为 string，导致所有消息事件在 json.Unmarshal 阶段整帧失败并被
// 静默丢弃——只有生命周期事件（没有 message 字段）能通过。
func Test_F04_MessageEventsWithArrayMessageReachSink(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got := make(chan []byte, 1)
	go func() { _ = c.Listen(ctx, func(raw []byte, _ Caller) { got <- raw }) }()

	frame := `{"time":1,"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","user_id":20002,"message_id":5,"message":[{"type":"text","data":{"text":"你好"}}],"sender":{"user_id":20002}}`
	if err := serverEnd.WriteMessage(ctx, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case raw := <-got:
		ev := event.NewEvent(raw)
		if ev.Kind != event.KindMessage || ev.Sub != "private" {
			t.Fatalf("event normalization: actual=(%q,%q) expected=(message,private)", ev.Kind, ev.Sub)
		}
		if got := ev.Message.PlainText(); got != "你好" {
			t.Fatalf("message text: actual=%q expected=%q", got, "你好")
		}
	case <-time.After(time.Second):
		t.Fatalf("message event was swallowed: frameErrors=%d", c.FrameErrors())
	}
	if n := c.FrameErrors(); n != 0 {
		t.Fatalf("frame errors: actual=%d expected=0", n)
	}
}

// Test_F04_MalformedFramesAreCountedNotSwallowed 保证解析失败可观测，且不影响读循环。
func Test_F04_MalformedFramesAreCountedNotSwallowed(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	var reported atomic.Int64
	c := NewWSClient("ws://in-memory", nil,
		WithDialer(func(ctx context.Context, rawURL string) (wsConn, error) { return clientEnd, nil }),
		WithFrameErrorHook(func(raw []byte, err error) { reported.Add(1) }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	events := make(chan []byte, 1)
	go func() { _ = c.Listen(ctx, func(raw []byte, _ Caller) { events <- raw }) }()

	if err := serverEnd.WriteMessage(ctx, []byte("this is not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "frame error reported", func() bool { return reported.Load() == 1 })
	if n := c.FrameErrors(); n != 1 {
		t.Fatalf("FrameErrors: actual=%d expected=1", n)
	}

	// 读循环必须还活着。
	if err := serverEnd.WriteMessage(ctx, []byte(`{"post_type":"meta_event","meta_event_type":"lifecycle"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatalf("read loop died after a malformed frame")
	}
}

// Test_F04_HeartbeatWithObjectStatusIsNotAFrameError 是第二条回归测试。
//
// 心跳帧的 status 是对象（online/good），API 回包的 status 是字符串。任何把整帧映射到
// 一个结构体的做法都会因这个字段整体失败——这正是继 message 之后暴露的第二个同类问题。
func Test_F04_HeartbeatWithObjectStatusIsNotAFrameError(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	events := make(chan []byte, 2)
	go func() { _ = c.Listen(ctx, func(raw []byte, _ Caller) { events <- raw }) }()

	heartbeat := `{"time":1,"self_id":10001,"post_type":"meta_event","meta_event_type":"heartbeat","status":{"online":true,"good":true},"interval":30000}`
	if err := serverEnd.WriteMessage(ctx, []byte(heartbeat)); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	waitFor(t, "heartbeat counted", func() bool { return c.Heartbeats() == 1 })
	select {
	case raw := <-events:
		t.Fatalf("heartbeat leaked to sink: %s", raw)
	case <-time.After(100 * time.Millisecond):
	}

	msg := `{"time":2,"self_id":10001,"post_type":"message","message_type":"private","sub_type":"friend","user_id":20002,"message_id":9,"message":[{"type":"text","data":{"text":"在吗"}}]}`
	if err := serverEnd.WriteMessage(ctx, []byte(msg)); err != nil {
		t.Fatalf("write message: %v", err)
	}
	select {
	case raw := <-events:
		ev := event.NewEvent(raw)
		if ev.Kind != event.KindMessage || ev.Message.PlainText() != `在吗` {
			t.Fatalf("event after heartbeat: actual=(%q,%q)", ev.Kind, ev.Message.PlainText())
		}
	case <-time.After(time.Second):
		t.Fatalf("message after heartbeat was swallowed")
	}
	if n := c.FrameErrors(); n != 0 {
		t.Fatalf("frame errors: actual=%d expected=0", n)
	}
}

// Test_F04_ResponseFramesStillParse 保证容错改造没有破坏 API 回包路径。
func Test_F04_ResponseFramesStillParse(t *testing.T) {
	t.Parallel()
	clientEnd, serverEnd := newPipePair()
	c := newTestClient(t, clientEnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	go func() { _ = c.Listen(ctx, func([]byte, Caller) {}) }()

	go func() {
		raw, err := serverEnd.ReadMessage(ctx)
		if err != nil {
			return
		}
		var rq Request
		_ = json.Unmarshal(raw, &rq)
		body, _ := json.Marshal(map[string]any{
			"status":  "ok",
			"retcode": 0,
			"message": "ok",
			"echo":    rq.Echo,
			"data":    map[string]any{"user_id": 10001},
		})
		_ = serverEnd.WriteMessage(ctx, body)
	}()

	resp, err := c.Call(ctx, Request{Action: "get_login_info"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Status != "ok" || resp.RetCode != 0 {
		t.Fatalf("response: actual=(%q,%d)", resp.Status, resp.RetCode)
	}
	if !strings.Contains(string(resp.Data), "10001") {
		t.Fatalf("response data: actual=%s", resp.Data)
	}
}
