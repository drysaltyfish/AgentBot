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

type frame struct {
	Status        string          `json:"status"`
	RetCode       int64           `json:"retcode"`
	Data          json.RawMessage `json:"data"`
	Message       string          `json:"message"`
	Wording       string          `json:"wording"`
	Echo          uint64          `json:"echo"`
	PostType      string          `json:"post_type"`
	MetaEventType string          `json:"meta_event_type"`
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
}

// WSClientOption 配置 WSClient。
type WSClientOption func(*WSClient)

// WithDialer 注入自定义拨号函数（测试用内存管道）。
func WithDialer(d DialFunc) WSClientOption {
	return func(c *WSClient) { c.dial = d }
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
	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		// 不认识的帧不该杀死读循环。
		return nil
	}
	if f.Echo != 0 {
		resp := Response{
			Status:  f.Status,
			Data:    f.Data,
			Message: f.Message,
			Wording: f.Wording,
			RetCode: f.RetCode,
			Echo:    f.Echo,
		}
		if v, ok := c.pending.LoadAndDelete(f.Echo); ok {
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
	if f.PostType == "" {
		return nil
	}
	if f.PostType == "meta_event" && f.MetaEventType == "heartbeat" {
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
