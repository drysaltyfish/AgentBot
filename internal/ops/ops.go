// Package ops 在独立回环地址上暴露健康/就绪探针与指标端点（FEATURES.md F-69）。
//
// 探针路径与默认端口只来自本包常量，部署清单必须引用同一来源，避免"配置抓取但没端点"。
package ops

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

// DefaultAddr 是指标/探针监听地址的默认值（仅回环）。
const DefaultAddr = "127.0.0.1:9090"

// 探针与指标路径。
const (
	PathHealthz = "/healthz"
	PathReadyz  = "/readyz"
	PathMetrics = "/metrics"
)

// Check 是一项依赖检查的结果。
type Check struct {
	// Name 是依赖名，如 "llm"、"store"、"driver"。
	Name string
	// OK 表示该依赖是否健康。
	OK bool
	// Err 是不健康时的可读原因。
	Err string
}

// Readiness 执行全部依赖检查；ctx 已带 ProbeTimeout 截止时间，实现应尽快返回。
type Readiness func(ctx context.Context) []Check

// Options 描述 Server 的构造参数。
type Options struct {
	// Addr 是监听地址；为空时用 DefaultAddr。
	Addr string
	// Registry 为 nil 时 /metrics 返回空正文。
	Registry *metrics.Registry
	// Ready 为 nil 时视为无依赖，就绪只看 SetReady。
	Ready Readiness
	// ProbeTimeout 限制单次探测；<=0 时默认 1s。
	ProbeTimeout time.Duration
	// CacheTTL 缓存就绪结果；<=0 时默认 10s。
	CacheTTL time.Duration
	// AuthToken 非空时，三个端点都要求 Authorization: Bearer <token>。
	AuthToken string
	// Log 可为 nil。
	Log *observe.Logger
}

// Server 是探针与指标 HTTP 服务。
type Server struct {
	opts  Options
	ready atomic.Bool

	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	addr string

	cacheMu     sync.Mutex
	cached      []Check
	cacheValid  bool
	cacheExpiry time.Time
}

// New 构造 Server，但不监听；须调用 Start。
func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = time.Second
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 10 * time.Second
	}
	return &Server{opts: opts}
}

// Start 立即在配置地址上监听，并在后台 goroutine 中服务。
func (s *Server) Start() error {
	s.mu.Lock()
	if s.srv != nil {
		s.mu.Unlock()
		return errors.New("ops: server already started")
	}
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("ops: listen %s: %w", s.opts.Addr, err)
	}
	// ReadHeaderTimeout 防 Slowloris（gosec G112）；探针端点只有回环访问，给 5s 足够。
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	s.srv = srv
	s.ln = ln
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if lg := s.logger(); lg != nil {
				lg.Error("ops: server stopped", "err", err)
			}
		}
	}()
	return nil
}

// Addr 返回实际绑定的地址；未启动时返回配置地址。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr != "" {
		return s.addr
	}
	return s.opts.Addr
}

// SetReady 标记进程是否已完成启动（starting 与 ready 的区分）。
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Close 优雅关闭 HTTP 服务；未启动时为 no-op。
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("ops: shutdown: %w", err)
	}
	return nil
}

// logger 返回组件 logger；未配置日志时为 nil。
func (s *Server) logger() *slog.Logger {
	if s.opts.Log == nil {
		return nil
	}
	return s.opts.Log.Component("ops")
}

// handler 组装三个端点的路由。
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealthz, s.handleHealthz)
	mux.HandleFunc(PathReadyz, s.handleReadyz)
	mux.HandleFunc(PathMetrics, s.handleMetrics)
	return mux
}

// authorize 校验可选的 Bearer 令牌。
func (s *Server) authorize(r *http.Request) bool {
	if s.opts.AuthToken == "" {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	token := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.AuthToken)) == 1
}

// handleHealthz 是存活探针：进程活着即 200，绝不触碰下游依赖。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

// handleReadyz 是就绪探针：未就绪或任一依赖失败时返回 503 并列出原因。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "not ready: starting\n")
		return
	}

	failed := make([]Check, 0)
	for _, c := range s.checkReady(r.Context()) {
		if !c.OK {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		for _, c := range failed {
			_, _ = io.WriteString(w, c.Name)
			if c.Err != "" {
				_, _ = io.WriteString(w, ": "+c.Err)
			}
			_, _ = io.WriteString(w, "\n")
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

// checkReady 返回缓存内的就绪结果；缓存过期时在 ProbeTimeout 内重新探测。
func (s *Server) checkReady(ctx context.Context) []Check {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	now := time.Now()
	if s.cacheValid && now.Before(s.cacheExpiry) {
		return s.cached
	}
	if s.opts.Ready == nil {
		s.cached = nil
	} else {
		pctx, cancel := context.WithTimeout(ctx, s.opts.ProbeTimeout)
		s.cached = s.opts.Ready(pctx)
		cancel()
	}
	s.cacheValid = true
	s.cacheExpiry = now.Add(s.opts.CacheTTL)
	return s.cached
}

// handleMetrics 以 Prometheus 文本格式输出指标。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if s.opts.Registry == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	var buf bytes.Buffer
	if err := s.opts.Registry.WritePrometheus(&buf); err != nil {
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = buf.WriteTo(w)
}
