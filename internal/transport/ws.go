package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// wsConn 是 WebSocket 的最小切面，便于用内存管道做契约测试。
type wsConn interface {
	WriteMessage(ctx context.Context, data []byte) error
	ReadMessage(ctx context.Context) ([]byte, error)
	Close() error
}

// DialFunc 建立一条 wsConn；测试可替换成内存管道。
type DialFunc func(ctx context.Context, url string) (wsConn, error)

// 信封解析必须是逐字段容错的，不能整帧映射到一个结构体。
//
// OneBot 的同名字段在不同帧里类型不同：
//   - message：事件帧是消息段数组，API 回包是字符串
//   - status：心跳帧是对象（online/good），API 回包是字符串
//
// 只要其中任意一个字段类型不符，整帧 json.Unmarshal 就会失败；早期版本因此把全部
// 消息事件静默丢掉（只有不含这些字段的生命周期帧能通过）。所以这里先解成
// map[string]json.RawMessage，再按字段各自容错取值。

// rawText 读取字符串字段；类型不是字符串时返回空串，不影响整帧解析。
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// rawUint 读取无符号整数字段。
func rawUint(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// rawInt 读取有符号整数字段。
func rawInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// WSClient 是正向 WebSocket 驱动，同时实现 Driver 与 Caller
// （FEATURES.md F-04 / F-06 / F-80）。
type WSClient struct {
	url        string
	auth       *Auth
	dial       DialFunc
	seq        atomic.Uint64
	pending    sync.Map
	writeMu    sync.Mutex
	mu         sync.Mutex
	conn       wsConn
	closed     bool
	heartbeats atomic.Uint64

	frameErrors  atomic.Uint64
	onFrameError func(raw []byte, err error)
}

// WSClientOption 配置 WSClient。
type WSClientOption func(*WSClient)

// WithDialer 注入自定义拨号函数（测试用内存管道）。
func WithDialer(d DialFunc) WSClientOption {
	return func(c *WSClient) { c.dial = d }
}

// WithFrameErrorHook 注入帧解析失败的回调。
//
// 解析失败的帧必须被计数并上报，绝不允许静默丢弃——否则协议不匹配会表现为
// 机器人完全没有反应，而日志里什么都没有。
func WithFrameErrorHook(fn func(raw []byte, err error)) WSClientOption {
	return func(c *WSClient) { c.onFrameError = fn }
}

// NewWSClient 构造正向 WS 客户端。
func NewWSClient(rawURL string, auth *Auth, opts ...WSClientOption) *WSClient {
	c := &WSClient{url: rawURL, auth: auth, dial: coderDial}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Connect 建立连接；重复调用且仍连着时是幂等的。
func (c *WSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	target := c.url
	if c.auth != nil {
		var err error
		target, err = c.auth.AuthorizeURL(c.url)
		if err != nil {
			return err
		}
	}
	conn, err := c.dial(ctx, target)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.url, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = conn.Close()
		return ErrClosed
	}
	c.conn = conn
	return nil
}

// Call 用自增 echo 把异步 WS 调用同步化。
func (c *WSClient) Call(ctx context.Context, req Request) (Response, error) {
	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return Response{}, ErrClosed
	}
	if conn == nil {
		return Response{}, ErrNoConnection
	}

	echo := c.seq.Add(1)
	req.Echo = echo
	ch := make(chan Response, 1)
	c.pending.Store(echo, ch)
	defer c.pending.Delete(echo)

	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	c.writeMu.Lock()
	werr := conn.WriteMessage(ctx, payload)
	c.writeMu.Unlock()
	if werr != nil {
		c.pending.Delete(echo)
		return Response{}, fmt.Errorf("write request: %w", werr)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

// Listen 读取帧：带 echo 的当作 API 回包，其余当作事件投递给 sink。
func (c *WSClient) Listen(ctx context.Context, sink Sink) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNoConnection
	}
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := conn.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read frame: %w", err)
		}
		if err := c.dispatch(raw, sink); err != nil {
			return err
		}
	}
}

func (c *WSClient) dispatch(raw []byte, sink Sink) error {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		// 连 JSON 都不是：计数并上报，但绝不杀死读循环。
		c.frameErrors.Add(1)
		if c.onFrameError != nil {
			c.onFrameError(raw, err)
		}
		return nil
	}

	if echo, ok := rawUint(env["echo"]); ok && echo != 0 {
		resp := Response{
			Status:  rawText(env["status"]),
			Data:    env["data"],
			Message: rawText(env["message"]),
			Wording: rawText(env["wording"]),
			Echo:    echo,
		}
		if code, ok := rawInt(env["retcode"]); ok {
			resp.RetCode = code
		}
		if v, ok := c.pending.LoadAndDelete(echo); ok {
			ch := v.(chan Response)
			select {
			case ch <- resp:
			default:
			}
			close(ch)
			return nil
		}
		// 无人等待的回包：丢弃（计数交由上层指标）。
		return nil
	}

	postType := rawText(env["post_type"])
	if postType == "" {
		return nil
	}
	if postType == "meta_event" && rawText(env["meta_event_type"]) == "heartbeat" {
		c.heartbeats.Add(1)
		return nil
	}
	if sink != nil {
		sink(raw, c)
	}
	return nil
}

// Heartbeats 返回已丢弃的心跳帧数量。
func (c *WSClient) Heartbeats() uint64 { return c.heartbeats.Load() }

// FrameErrors 返回无法解析的帧数量（应当恒为 0）。
func (c *WSClient) FrameErrors() uint64 { return c.frameErrors.Load() }

// PendingCalls 返回尚未完成的调用数，用于断言没有泄漏。
func (c *WSClient) PendingCalls() int {
	n := 0
	c.pending.Range(func(any, any) bool { n++; return true })
	return n
}

// Close 关闭底层连接。
func (c *WSClient) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func coderDial(ctx context.Context, rawURL string) (wsConn, error) {
	conn, _, err := websocket.Dial(ctx, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return &coderConn{conn: conn}, nil
}

type coderConn struct {
	conn *websocket.Conn
}

func (c *coderConn) WriteMessage(ctx context.Context, data []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *coderConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, data, err := c.conn.Read(ctx)
	return data, err
}

func (c *coderConn) Close() error { return c.conn.CloseNow() }

var (
	_ Driver = (*WSClient)(nil)
	_ Caller = (*WSClient)(nil)
	_ Closer = (*WSClient)(nil)
)
