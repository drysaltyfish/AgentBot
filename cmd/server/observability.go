package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drysaltyfish/agentbot/internal/access"
	"github.com/drysaltyfish/agentbot/internal/admin"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/moderation"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/ops"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/reload"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/scoped"
	"github.com/drysaltyfish/agentbot/internal/secrets"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/textguard"
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
	// cost 是 F-66 的成本统计器；nil 表示未启用。
	cost *cost.Tracker
	// budget 是 F-32 的上下文预算；nil 表示不做预算裁剪。
	budget *llm.Budget
	// warn 是降级路径的告警出口（例如 downgrade 动作未能强制）；nil 时静默。
	warn func(string)
}

var _ llm.LLM = (*observedLLM)(nil)

// Chat 记录一次非流式调用的状态、延迟与 token，并在调用前执行配额判定。
func (o *observedLLM) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := o.authorize(ctx); err != nil {
		return nil, err
	}
	// F-32：在真正发出前裁剪，超长历史不会变成一次被服务端拒绝的往返。
	if o.budget != nil {
		req = o.budget.FitRequest(ctx, req)
	}
	start := time.Now()
	resp, err := o.next.Chat(ctx, req)
	o.observe(ctx, start, resp, err)
	return resp, err
}

// ChatStream 透传流式调用；流式用量在 provider 侧已计入台账，这里只记请求数。
func (o *observedLLM) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.Chunk, error) {
	if err := o.authorize(ctx); err != nil {
		return nil, err
	}
	// F-32：流式同样先裁剪——两条路径必须看到同一份消息序列。
	if o.budget != nil {
		req = o.budget.FitRequest(ctx, req)
	}
	start := time.Now()
	ch, err := o.next.ChatStream(ctx, req)
	o.observe(ctx, start, nil, err)
	return ch, err
}

// authorize 在真正花钱之前执行配额判定（F-66）。
//
// 这是唯一能"拒绝请求"的位置：拦在这里，被拒绝的调用不会产生任何 provider 费用。
// deny 时把 *cost.QuotaError 原样向上传——回复层靠 errors.As 认出它并给用户提示。
//
// downgrade 目前**未强制**：按请求切模型需要 llm.ChatRequest 带 model 字段，
// 而那是协议形状的改动。宁可在此如实告警，也不假装降级生效了。
func (o *observedLLM) authorize(ctx context.Context) error {
	if o.cost == nil {
		return nil
	}
	att := cost.AttributionFrom(ctx)
	dec, err := o.cost.Authorize(att.SessionKey, att.UserID)
	if err == nil {
		if dec.Action == cost.ActionDowngrade && o.warn != nil {
			o.warn(fmt.Sprintf("cost downgrade is configured but not enforced (per-request model switching is unsupported): suggested=%q", dec.DowngradeModel))
		}
		return nil
	}
	return fmt.Errorf("llm call denied by cost quota: %w", err)
}

func (o *observedLLM) observe(ctx context.Context, start time.Time, resp *llm.ChatResponse, err error) {
	// F-66：按 provider 真实 usage 记账（不做估算），并带上 ctx 里的会话/用户归属。
	// 必须放在指标之前：否则 cat 为 nil 时会把成本一起吞掉（静默丢账比指标缺失严重）。
	if o.cost != nil && resp != nil {
		att := cost.AttributionFrom(ctx)
		_, _ = o.cost.Record(cost.Call{
			Provider:         o.provider,
			Model:            o.model,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			Latency:          time.Since(start),
			SessionKey:       att.SessionKey,
			UserID:           att.UserID,
			Timestamp:        time.Now(),
		})
	}
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
		Redactor:     secrets.Scrub,
		Warn:         func(msg string) { alog.Warn(msg) },
	}), closer
}

// resolveAPIKey 按 F-61 的优先级解析密钥：环境变量 > 密钥文件 > 配置内联。
//
// warning 需要记录但不阻断启动；error 只在"配了密钥文件却读不到"时出现。
func resolveAPIKey(cfg *config.Config) (string, string, error) {
	v, err := secrets.Resolve(secrets.ResolveOptions{
		EnvVar:   stringOr(cfg.LLM.APIKeyEnv, ""),
		FilePath: stringOr(cfg.LLM.APIKeyFile, ""),
		Inline:   stringOr(cfg.LLM.APIKey, ""),
	})
	if err != nil {
		return "", "", err
	}
	return v.Secret, v.Warning, nil
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

// buildModeration 构造入站审查引擎（F-57 / F-58）；未启用时返回 nil。
//
// 审计与指标注入放在组合根（而不是 engine 的回调里）：决策已经带着 Rule/Reason，
// 在这里既省一层适配，也让"什么算拦截"只有一个判据。
func buildModeration(cfg *config.Config, lg *observe.Logger) (*moderation.Engine, error) {
	if !cfg.Moderation.EffectiveEnabled() {
		return nil, nil
	}
	mlog := lg.Component("moderation")

	words, err := loadSensitiveWords(cfg)
	if err != nil {
		return nil, err
	}
	var matcher *textguard.Engine
	if len(words) > 0 {
		rules := make([]textguard.Rule, 0, len(words))
		for _, w := range words {
			rules = append(rules, textguard.Rule{Word: w})
		}
		replacement := "***"
		if cfg.Moderation.MaskReplacement != nil {
			replacement = *cfg.Moderation.MaskReplacement
		}
		m, merr := textguard.New(rules, textguard.Options{DefaultReplacement: replacement, Normalize: true})
		if merr != nil {
			return nil, fmt.Errorf("compile sensitive words: %w", merr)
		}
		matcher = textguard.NewEngine(m)
	}

	bans, berr := buildBlacklist(cfg, func(msg string) { mlog.Warn(msg) })
	if berr != nil {
		return nil, berr
	}

	sensitive := moderation.SensitiveMask
	if cfg.Moderation.EffectiveAction() == "block" {
		sensitive = moderation.SensitiveBlock
	}
	spam := moderation.NewAntiSpam(moderation.AntiSpamConfig{
		Window:          cfg.Moderation.EffectiveSpamWindow(),
		MaxMessages:     cfg.Moderation.EffectiveSpamMaxMessages(),
		BanDuration:     cfg.Moderation.EffectiveBanDuration(),
		DuplicateRepeat: cfg.Moderation.EffectiveDuplicateRepeat(),
	})
	mlog.Info("inbound moderation is enabled",
		"words", len(words), "action", cfg.Moderation.EffectiveAction(),
		"spam_window", cfg.Moderation.EffectiveSpamWindow().String(),
		"spam_max", cfg.Moderation.EffectiveSpamMaxMessages(),
		"blacklist_file", cfg.Moderation.BlacklistFile)

	return moderation.New(moderation.Options{
		Matcher:   matcher,
		Sensitive: sensitive,
		Blacklist: bans,
		AntiSpam:  spam,
		Warn:      func(msg string) { mlog.Warn(msg) },
	}), nil
}

// buildBlacklist 构造黑名单；文件不可用时退回内存并告警（可用性优先）。
func buildBlacklist(cfg *config.Config, warn func(string)) (*moderation.Blacklist, error) {
	opts := moderation.BlacklistOptions{
		SelfID: cfg.Transport.EffectiveSelfID(),
		// 永不可封禁名单 = 超管名单（唯一来源：access.roles.superuser）。
		SuperUsers: access.NewRoles(cfg.Access.Roles).SuperUsers(),
		Warn:       warn,
	}
	if path := strings.TrimSpace(cfg.Moderation.BlacklistFile); path != "" {
		store, err := moderation.NewFileBanStore(path)
		if err == nil {
			return moderation.NewBlacklist(store, opts)
		}
		warn(fmt.Sprintf("cannot open blacklist store; using memory (state will not survive restart): %v (path=%s)", err, path))
	}
	return moderation.NewBlacklist(moderation.NewMemoryBanStore(), opts)
}

// loadSensitiveWords 合并内联词表与文件词表；文件按行读，忽略空行与 # 注释。
func loadSensitiveWords(cfg *config.Config) ([]string, error) {
	words := append([]string{}, cfg.Moderation.SensitiveWords...)
	path := strings.TrimSpace(cfg.Moderation.SensitiveWordsFile)
	if path == "" {
		return words, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sensitive words %s: %w", path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words = append(words, line)
	}
	return words, nil
}

// moderationPreHook 把入站审查挂成 pre 钩子：拦截的事件不进入任何路由。
//
// roleOf 由组合根注入（含 access.roles 的显式指定），审查据此判定"这次发言是谁送的"。
func moderationPreHook(eng *moderation.Engine, cat *metrics.Catalog, alog *audit.Logger, lg *observe.Logger, roleOf func(*event.Event) agent.Role) router.Rule {
	mlog := lg.Component("moderation")
	return func(c *router.Ctx) bool {
		if c == nil || c.Event == nil {
			return true
		}
		decision, err := eng.Review(c, moderation.Message{Text: c.MessageString()}, moderation.Meta{
			UserID:    c.Event.UserID,
			GroupID:   c.Event.GroupID,
			SelfID:    c.Event.SelfID,
			Role:      string(roleOf(c.Event)),
			Addressed: c.Event.GroupID == 0 || router.AtMe()(c),
		})
		if err != nil {
			// 规格要求 guard 超时/失败默认放行：审查是保护措施，不该变成故障点。
			mlog.Warn("review failed; allowing the message", "error", err)
			return true
		}
		if !decision.Blocked() {
			return true
		}
		if cat != nil {
			cat.GuardBlocks.With(metrics.Labels{"guard": "moderation", "reason": decision.Rule}).Inc()
		}
		if alog != nil {
			alog.Log(audit.Event{
				Type: audit.EventInboundBlocked, TraceID: observe.TraceID(c),
				UserID: c.Event.UserID, GroupID: c.Event.GroupID,
				Action: decision.Rule, Result: audit.ResultDenied,
				Params: map[string]string{"reason": decision.Reason},
			})
		}
		mlog.Info("inbound message blocked",
			"rule", decision.Rule, "reason", decision.Reason,
			"user_id", c.Event.UserID, "group_id", c.Event.GroupID)
		return false
	}
}

// watchSensitiveWords 热加载敏感词表（F-24 里点名的"敏感词表"一项）。
//
// 文件变化 -> 重新编译 AC 自动机 -> SwapMatcher 原子替换（不阻塞 Review）。
// 编译失败时保留旧词表并告警：词表写错的代价不该是"审核失效"。
func watchSensitiveWords(ctx context.Context, path string, replacement *string, eng *moderation.Engine, lg *observe.Logger) *reload.Watcher[*textguard.Matcher] {
	if eng == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	mlog := lg.Component("moderation")
	rep := "***"
	if replacement != nil {
		rep = *replacement
	}
	build := func() (*textguard.Matcher, error) { return compileWordsFile(path, rep) }

	var w *reload.Watcher[*textguard.Matcher]
	w = reload.New([]string{path}, build, reload.Options{
		OnSwap: func(version uint64, _ string) {
			if m, ok := w.Current(); ok {
				eng.SwapMatcher(m)
				mlog.Info("sensitive words reloaded", "version", version)
			}
		},
		Warn: func(err error) {
			mlog.Warn("sensitive words reload failed; keeping the previous list", "error", err)
		},
	})
	w.Start(ctx)
	return w
}

// compileWordsFile 从词表文件编译自动机（忽略空行与 # 注释）。
func compileWordsFile(path, replacement string) (*textguard.Matcher, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sensitive words %s: %w", path, err)
	}
	var rules []textguard.Rule
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, textguard.Rule{Word: line})
	}
	return textguard.New(rules, textguard.Options{DefaultReplacement: replacement, Normalize: true})
}

// buildCostTracker 构造成本统计器（F-66）。
func buildCostTracker(cfg *config.Config, lg *observe.Logger, cat *metrics.Catalog, persist cost.Store) (*cost.Tracker, error) {
	_ = cat // 指标接缝预留给后续（当前由 metrics Catalog 之外的调用方使用）
	prices := cost.PriceTable{Prices: map[string]cost.Price{}, Unknown: cost.UnknownWarnZero}
	if cfg.Cost.EffectiveUnknownModel() == "reject" {
		prices.Unknown = cost.UnknownReject
	}
	for model, p := range cfg.Cost.Prices {
		prices.Prices[model] = cost.Price{InputPer1K: p.InputPer1K, OutputPer1K: p.OutputPer1K}
	}
	quotas := make([]cost.Quota, 0, len(cfg.Cost.Quotas))
	for _, q := range cfg.Cost.Quotas {
		quotas = append(quotas, cost.Quota{
			Scope:          cost.Scope(q.Scope),
			Period:         cost.Period(q.Period),
			Limit:          q.Limit,
			SoftLimit:      q.SoftLimit,
			Action:         cost.Action(q.Action),
			DowngradeModel: q.DowngradeModel,
		})
	}
	t, err := cost.New(cost.Options{
		Prices:    prices,
		Quotas:    quotas,
		Store:     persist,
		QueueSize: cfg.Cost.EffectiveQueueSize(),
		Warn:      func(msg string) { lg.Component("cost").Warn(msg) },
	})
	if err != nil {
		return nil, fmt.Errorf("build cost tracker: %w", err)
	}
	lg.Component("cost").Info("cost tracking enabled",
		"models", len(prices.Prices), "quotas", len(quotas),
		"unknown_model", cfg.Cost.EffectiveUnknownModel(), "persistent", persist != nil)
	return t, nil
}

// buildAdminModule 构造管理命令模块（F-71）。
//
// 只注册组合根有能力提供数据的命令；/help 由 admin.New 自带。
// 鉴权统一走 moderation.super_users：仓库里已经有"谁说了算"的配置，
// 不该再造第二份。
func buildAdminModule(cfg *config.Config, alog *audit.Logger, lg *observe.Logger, mod *moderation.Engine, costTracker *cost.Tracker, personas *scoped.Manager, promptHash admin.PromptHashFunc) *admin.Module {
	// 超管名单只有一处：access.roles.superuser（与组合根同一份）。
	supers := make(map[int64]bool)
	for id := range superUsersFrom(access.NewRoles(cfg.Access.Roles)) {
		supers[id] = true
	}
	mlog := lg.Component("admin")

	m := admin.New(admin.Options{
		Checker: func(_ context.Context, inv admin.Invocation) bool { return supers[inv.UserID] },
		Auditor: func(ev admin.AuditEvent) {
			if alog == nil {
				return
			}
			res := audit.ResultDenied
			if ev.Authorized {
				res = audit.ResultOK
			}
			alog.Log(audit.Event{
				Type:    audit.EventAdminCommand,
				UserID:  ev.UserID,
				GroupID: ev.GroupID,
				Action:  ev.Command,
				Result:  res,
			})
		},
		DenyMode: admin.DenyExplicit,
	})

	if mod != nil {
		bans := mod.Blacklist()
		if bans != nil {
			_ = m.Register("ban", "/ban <用户号> [原因] —— 永久封禁", func(_ context.Context, inv admin.Invocation) (string, error) {
				if len(inv.Args) == 0 {
					return "", fmt.Errorf("%w: 需要用户号", admin.ErrUsage)
				}
				reason := "管理员指令"
				if len(inv.Args) > 1 {
					reason = strings.Join(inv.Args[1:], " ")
				}
				if err := bans.Ban(moderation.BanUser, inv.Args[0], reason, 0); err != nil {
					return "", fmt.Errorf("ban: %w", err)
				}
				return "已封禁 " + inv.Args[0], nil
			})
			_ = m.Register("unban", "/unban <用户号> —— 解除封禁", func(_ context.Context, inv admin.Invocation) (string, error) {
				if len(inv.Args) == 0 {
					return "", fmt.Errorf("%w: 需要用户号", admin.ErrUsage)
				}
				ok, err := bans.Unban(moderation.BanUser, inv.Args[0])
				if err != nil {
					return "", fmt.Errorf("unban: %w", err)
				}
				if !ok {
					return "该用户不在黑名单里", nil
				}
				return "已解封 " + inv.Args[0], nil
			})
			_ = m.Register("banlist", "/banlist —— 列出当前封禁", func(_ context.Context, _ admin.Invocation) (string, error) {
				entries := bans.List()
				if len(entries) == 0 {
					return "黑名单为空", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "黑名单 %d 条：", len(entries))
				for i, e := range entries {
					if i >= 20 {
						b.WriteString(" …")
						break
					}
					fmt.Fprintf(&b, " %s", e.ID)
				}
				return b.String(), nil
			})
		}
	}

	if costTracker != nil {
		if err := m.Register("cost", "/cost —— 今日、累计与本会话的调用次数与费用", func(_ context.Context, inv admin.Invocation) (string, error) {
			today := costTracker.Today()
			all := costTracker.Global()
			// F-66：会话维度就是"谁在问"这一路——用调用者的会话键，而不是某个全局值。
			key := session.Key{SelfID: cfg.Transport.EffectiveSelfID(), GroupID: inv.GroupID, UserID: inv.UserID}
			sess := costTracker.Session(key.String())
			return fmt.Sprintf("今日：%d 次调用 / $%.4f\n累计：%d 次调用 / $%.4f\n本会话：%d 次调用 / $%.4f",
				today.Calls, today.Cost, all.Calls, all.Cost, sess.Calls, sess.Cost), nil
		}); err != nil {
			mlog.Warn("register cost command", "error", err)
		}
	}

	// F-82：人格切换入口。只改持久化的作用域键，不重建会话。
	if err := registerPersonaCommand(m, personas, cfg.Transport.EffectiveSelfID()); err != nil {
		mlog.Warn("register persona command", "error", err)
	}
	// F-65：/prompt-hash——按调用者会话报告三段哈希。
	if promptHash != nil {
		if err := m.RegisterBuiltins(admin.Builtins{PromptHash: promptHash}); err != nil {
			mlog.Warn("register prompt-hash command", "error", err)
		}
	}

	mlog.Info("admin commands registered", "commands", len(m.Commands()))
	return m
}
