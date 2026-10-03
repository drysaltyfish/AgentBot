package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/ops"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/toggle"
)

// providerName 返回用于指标标签的 provider 名（与 buildLLM 的默认分支保持一致）。
func providerName(cfg *config.Config) string {
	p := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	if p == "" {
		return "openai"
	}
	return p
}

// observedLLM 用装饰器收集模型调用的请求数、token 与延迟（F-68）。
//
// 用装饰器而不是改 internal/llm：指标是横切关注点，模型实现不该知道指标。
type observedLLM struct {
	next     llm.LLM
	cat      *metrics.Catalog
	provider string
	model    string
}

var _ llm.LLM = (*observedLLM)(nil)

// Chat 记录一次非流式调用的状态、延迟与 token。
func (o *observedLLM) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	start := time.Now()
	resp, err := o.next.Chat(ctx, req)
	o.observe(start, resp, err)
	return resp, err
}

// ChatStream 透传流式调用；流式用量在 provider 侧已计入台账，这里只记请求数。
func (o *observedLLM) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.Chunk, error) {
	start := time.Now()
	ch, err := o.next.ChatStream(ctx, req)
	o.observe(start, nil, err)
	return ch, err
}

func (o *observedLLM) observe(start time.Time, resp *llm.ChatResponse, err error) {
	if o.cat == nil {
		return
	}
	status := "ok"
	if err != nil {
		status = "error"
	}
	labels := metrics.Labels{"provider": o.provider, "model": o.model}
	o.cat.LLMRequests.With(metrics.Labels{"provider": o.provider, "model": o.model, "status": status}).Inc()
	o.cat.LLMLatency.With(labels).Observe(time.Since(start).Seconds())
	if resp != nil {
		o.cat.LLMTokens.With(metrics.Labels{"provider": o.provider, "model": o.model, "type": "input"}).
			Add(float64(resp.Usage.PromptTokens))
		o.cat.LLMTokens.With(metrics.Labels{"provider": o.provider, "model": o.model, "type": "output"}).
			Add(float64(resp.Usage.CompletionTokens))
	}
}

// routeMetrics 把 router 的观测口接到指标目录（F-68）。
type routeMetrics struct{ cat *metrics.Catalog }

// RouteMatched 记一次路由匹配与处理耗时。
func (r routeMetrics) RouteMatched(route string, d time.Duration) {
	if r.cat == nil {
		return
	}
	if route == "" {
		route = "unnamed"
	}
	r.cat.RouteMatches.With(metrics.Labels{"route": route}).Inc()
	r.cat.HandlerDuration.With(metrics.Labels{"route": route}).Observe(d.Seconds())
}

// RoutePanicked 记一次被恢复的 Handler panic。
func (r routeMetrics) RoutePanicked(route string) {
	if r.cat == nil {
		return
	}
	if route == "" {
		route = "unnamed"
	}
	r.cat.RouteErrors.With(metrics.Labels{"route": route}).Inc()
}

// buildAudit 构造审计日志（F-60）。
//
// 审计按规格**不可关闭**：默认落文件，文件不可用时退回 stdout 并告警——
// 宁可写在错误的地方，也不能因为磁盘问题就没有审计。
// 返回值里的 io.Closer 是审计文件句柄（可能为 nil）。
func buildAudit(cfg *config.Config, lg *observe.Logger) (*audit.Logger, io.Closer) {
	alog := lg.Component("audit")
	var (
		writers []io.Writer
		closer  io.Closer
	)
	if path := strings.TrimSpace(cfg.Audit.File); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			alog.Warn("cannot open audit file; falling back to stdout", "error", err, "path", path)
		} else {
			writers = append(writers, f)
			closer = f
		}
	}
	if cfg.Audit.Stdout || len(writers) == 0 {
		writers = append(writers, os.Stdout)
	}
	return audit.New(audit.Options{
		Writer:       io.MultiWriter(writers...),
		QueueSize:    cfg.Audit.EffectiveQueueSize(),
		ContentLimit: cfg.Audit.EffectiveContentLimit(),
		Redactor:     config.Redact,
		Warn:         func(msg string) { alog.Warn(msg) },
	}), closer
}

// readinessChecks 构造 F-69 的就绪检查：存储可查、传输已连、provider 已配置。
//
// 不在这里做真实模型调用：探针每 10s 一次，不能把它变成对上游的压测。
func readinessChecks(st *store.Store, wsUp *atomic.Bool, provider string) ops.Readiness {
	return func(ctx context.Context) []ops.Check {
		checks := make([]ops.Check, 0, 3)

		err := st.Ping(ctx)
		checks = append(checks, ops.Check{Name: "storage", OK: err == nil, Err: errText(err)})

		up := wsUp != nil && wsUp.Load()
		checks = append(checks, ops.Check{Name: "transport", OK: up, Err: pick(!up, "websocket 未连接")})

		configured := strings.TrimSpace(provider) != ""
		checks = append(checks, ops.Check{Name: "llm", OK: configured, Err: pick(!configured, "llm.provider 未配置")})

		return checks
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pick(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// buildOps 启动指标/探针监听（F-68 / F-69）；未启用时返回 nil。
func buildOps(cfg *config.Config, cat *metrics.Catalog, ready ops.Readiness, lg *observe.Logger) (*ops.Server, error) {
	if !cfg.Ops.EffectiveEnabled() {
		lg.Component("ops").Info("metrics and probes are disabled")
		return nil, nil
	}
	srv := ops.New(ops.Options{
		Addr:         cfg.Ops.EffectiveAddr(),
		Registry:     cat.Registry,
		Ready:        ready,
		ProbeTimeout: cfg.Ops.EffectiveProbeTimeout(),
		CacheTTL:     cfg.Ops.EffectiveReadyCacheTTL(),
		AuthToken:    stringOr(cfg.Ops.AuthToken, ""),
		Log:          lg,
	})
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("start ops listener on %s: %w", cfg.Ops.EffectiveAddr(), err)
	}
	lg.Component("ops").Info("metrics and probes are listening",
		"addr", srv.Addr(), "metrics", ops.PathMetrics, "healthz", ops.PathHealthz, "readyz", ops.PathReadyz)
	return srv, nil
}

// closeOps 关闭监听；启动失败时为 nil，需要容忍。
func closeOps(srv *ops.Server, lg *observe.Logger) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		lg.Component("ops").Warn("cannot close ops listener", "error", err)
	}
}

// outboundAuditHook 保留出口过滤日志，并把丢弃计入 guard_blocks（F-68）。
func outboundAuditHook(cat *metrics.Catalog, lg *observe.Logger) func(outbound.AuditRecord) {
	olog := lg.Component("outbound")
	return func(rec outbound.AuditRecord) {
		olog.Info("outbound",
			"group_id", rec.GroupID, "user_id", rec.UserID,
			"dropped", rec.Dropped, "reason", rec.Reason, "runes", len([]rune(rec.Filtered)))
		if cat != nil && rec.Dropped {
			reason := rec.Reason
			if reason == "" {
				reason = "unknown"
			}
			cat.GuardBlocks.With(metrics.Labels{"guard": "outbound", "reason": reason}).Inc()
		}
	}
}

// rateLimitedHook 把一次限速拒绝同时记进指标与审计（F-18 / F-60 / F-68）。
func rateLimitedHook(cat *metrics.Catalog, alog *audit.Logger, scope string) func(*router.Ctx) {
	return func(c *router.Ctx) {
		if cat != nil {
			cat.RateLimited.With(metrics.Labels{"scope": scope}).Inc()
		}
		if alog != nil {
			alog.Log(audit.Event{
				Type: audit.EventRateLimited, Action: "rate_limit", Result: audit.ResultDenied,
				UserID: c.Event.UserID, GroupID: c.Event.GroupID,
				Params: map[string]string{"scope": scope},
			})
		}
	}
}

// togglePlugins 是可以被 /switch 关掉的插件名（= 路由名）。启动期注册，
// 使"开一个不存在的开关"在写入时就被拒绝（F-19）。
var togglePlugins = []string{"reply", "record", "switch"}

// newToggles 构造功能开关（F-19）。未启用时用内存 store 且默认全开，等价于没有开关。
func newToggles(cfg *config.Config, lg *observe.Logger) *toggle.Toggle {
	tlog := lg.Component("toggle")
	if !cfg.Toggle.EffectiveEnabled() {
		t := toggle.New(toggle.NewMemoryStore(), true, func(msg string) { tlog.Warn(msg) })
		t.Register(togglePlugins...)
		return t
	}
	var st toggle.Store = toggle.NewMemoryStore()
	if path := strings.TrimSpace(cfg.Toggle.File); path != "" {
		fst, err := toggle.NewFileStore(path)
		if err != nil {
			tlog.Warn("cannot open toggle store; using memory (state will not survive restart)",
				"error", err, "path", path)
		} else {
			st = fst
		}
	}
	t := toggle.New(st, cfg.Toggle.EffectiveDefaultOn(), func(msg string) { tlog.Warn(msg) })
	t.Register(togglePlugins...)
	tlog.Info("feature toggles are enabled", "plugins", togglePlugins, "file", cfg.Toggle.File)
	return t
}

// approvalAuditHook 把审批与策略拒绝写进审计（F-60）。
//
// 策略拒绝走 policy_denied、人工审批走 approval：两者都是"谁在什么时候
// 让机器人做了什么"里最需要留痕的部分。
func approvalAuditHook(alog *audit.Logger) func(agent.ApprovalRecord) {
	return func(rec agent.ApprovalRecord) {
		if alog == nil {
			return
		}
		typ := audit.EventApproval
		result := audit.ResultOK
		if !rec.Allowed {
			typ = audit.EventPolicyDenied
			result = audit.ResultDenied
		}
		alog.Log(audit.Event{
			Type: typ, Action: rec.Request.ToolName, Result: result,
			UserID: rec.Request.UserID, GroupID: rec.Request.GroupID,
			DurationMS: rec.WaitMS,
			Params:     map[string]string{"reason": rec.Reason, "verdict": rec.Verdict.String()},
		})
	}
}
