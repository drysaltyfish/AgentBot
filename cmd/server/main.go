// AgentBot 的组合根：读配置 → 装配 → 启动 → 优雅关闭（FEATURES.md F-25 / F-70）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/retry"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/tool"
	"github.com/drysaltyfish/agentbot/internal/tool/builtin"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// replyWorkers 是回复工作池大小；LLM 调用不应阻塞事件读循环。
const replyWorkers = 4

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agentbot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	checkOnly := fs.Bool("check-config", false, "只校验配置并打印生效配置（脱敏）后退出，不启动服务")
	selfTest := fs.Int64("selftest", 0, "连接平台后向该 QQ 号发送一条自检消息，然后退出")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	if *checkOnly {
		out, err := cfg.RedactedYAML()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprint(stdout, out)
		return 0
	}
	if *selfTest != 0 {
		return runSelfTest(cfg, *selfTest, stderr)
	}
	return serve(cfg, stderr)
}

func shutdownTimeout(cfg *config.Config) time.Duration {
	if cfg.Shutdown.Timeout != nil && cfg.Shutdown.Timeout.D > 0 {
		return cfg.Shutdown.Timeout.D
	}
	return 10 * time.Second
}

func queueSize(cfg *config.Config) int {
	if cfg.Log.QueueSize != nil {
		return *cfg.Log.QueueSize
	}
	return 1024
}

func stringOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

func configuredSelfID(cfg *config.Config) int64 {
	if cfg.Transport.SelfID == nil {
		return 0
	}
	return *cfg.Transport.SelfID
}

// openAIDefaultBase 是 openai provider 的默认端点，用于判断 base_url 是否被显式配置过。
const openAIDefaultBase = "https://api.openai.com/v1"

// buildLLM 按配置选择模型实现。echo 是联调用的假实现。
func buildLLM(cfg *config.Config, lg *observe.Logger) (llm.LLM, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	switch provider {
	case "echo":
		lg.Component("llm").Warn("using the echo provider: replies are a fixed template, not a real model")
		return llm.NewEcho(""), nil
	case "", "openai", "deepseek":
		client := httpx.NewClient(httpx.Config{
			Timeout:      llmTimeout(cfg),
			MaxBytes:     httpx.Defaults().MaxBytes,
			MaxRedirects: 3,
			// 本地/内网 provider（如 Ollama）需要显式放行；默认按 F-59 拒绝私网。
			AllowPrivate: false,
		})
		base := llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL:            baseURL(cfg, provider),
			APIKey:             stringOr(cfg.LLM.APIKey, ""),
			Model:              cfg.LLM.Model,
			Client:             client,
			Thinking:           cfg.LLM.Thinking,
			ReasoningEffort:    cfg.LLM.ReasoningEffort,
			IncludeStreamUsage: true,
		})
		return llm.NewRetryLLM(base, retry.Default()), nil
	default:
		return nil, fmt.Errorf("unsupported llm.provider %q", cfg.LLM.Provider)
	}
}

// baseURL 在未显式配置时给出该 provider 的默认端点。
//
// 注意：config.Default() 会把 base_url 预置成 OpenAI 的地址，所以 deepseek 必须同时
// 识别"为空"与"仍是 OpenAI 默认值"两种情况，否则会静默打到错误的端点。
func baseURL(cfg *config.Config, provider string) string {
	u := strings.TrimSpace(cfg.LLM.BaseURL)
	if provider == "deepseek" && (u == "" || u == openAIDefaultBase) {
		return "https://api.deepseek.com"
	}
	if u == "" {
		return openAIDefaultBase
	}
	return u
}

// systemPrompt 返回不可变前缀正文。
//
// 文件形式在启动时读一次就固定下来——之后任何时刻读文件都可能拿到改动后的内容，
// 那会让前缀在运行中变化，缓存全部失效。
func systemPrompt(cfg *config.Config) (string, error) {
	if path := strings.TrimSpace(stringOr(cfg.LLM.SystemPromptFile, "")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read llm.system_prompt_file %s: %w", path, err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", fmt.Errorf("llm.system_prompt_file %s is empty", path)
		}
		return s, nil
	}
	if cfg.LLM.SystemPrompt != nil && strings.TrimSpace(*cfg.LLM.SystemPrompt) != "" {
		return *cfg.LLM.SystemPrompt, nil
	}
	return conversation.DefaultSystemPrompt, nil
}

// historyTurns 返回最多回灌的历史条数。
func historyTurns(cfg *config.Config) int {
	if cfg.LLM.HistoryTurns != nil {
		return *cfg.LLM.HistoryTurns
	}
	return 20
}

// replyRule 把 behavior 配置翻译成路由谓词。
//
// 未配置时的默认：私聊 always、群聊 on_mention（群里不 @ 就不回复，避免刷屏），
// 且绝不回复机器人自己。
func replyRule(cfg *config.Config) router.Rule {
	private := cfg.Behavior.Private
	if private == "" {
		private = config.ReplyAlways
	}
	group := cfg.Behavior.Group
	if group == "" {
		group = config.ReplyOnMention
	}
	atMe := router.AtMe()
	return func(c *router.Ctx) bool {
		if c.Event == nil || c.Event.UserID == 0 || c.Event.UserID == c.Event.SelfID {
			return false
		}
		if c.Event.GroupID == 0 {
			return private == config.ReplyAlways
		}
		switch group {
		case config.ReplyAlways:
			return true
		case config.ReplyOnMention:
			return atMe(c)
		default:
			return false
		}
	}
}

func llmTimeout(cfg *config.Config) time.Duration {
	if cfg.LLM.Timeout != nil && cfg.LLM.Timeout.D > 0 {
		return cfg.LLM.Timeout.D
	}
	return 30 * time.Second
}

func intOr(p *int, fallback int) int {
	if p != nil && *p > 0 {
		return *p
	}
	return fallback
}

func boolOr(p *bool, fallback bool) bool {
	if p != nil {
		return *p
	}
	return fallback
}

func durationOr(p *config.Duration, fallback time.Duration) time.Duration {
	if p != nil && p.D > 0 {
		return p.D
	}
	return fallback
}

// agentRole 把平台上报的成员角色映射成 F-45 的权限角色。
func agentRole(ev *event.Event) agent.Role {
	if ev.GroupID == 0 {
		return agent.RolePrivate
	}
	switch ev.Sender.Role {
	case "owner":
		return agent.RoleOwner
	case "admin":
		return agent.RoleAdmin
	default:
		return agent.RoleMember
	}
}

// buildAgent 按配置装配 Agent（F-35 + F-41 + F-44 + F-45）。
//
// 返回的是 agent.Agent 接口：未启用 ReAct 时返回 DirectAgent，
// 因此调用方对两条路径完全同形，不需要分支。
func buildAgent(cfg *config.Config, model llm.LLM, sysPrompt string, hist history.History, lg *observe.Logger) (agent.Agent, error) {
	if !cfg.Agent.Enabled {
		return &agent.DirectAgent{LLM: model, SystemPrompt: sysPrompt}, nil
	}

	registry := tool.New(tool.WithWarnFunc(func(msg string) {
		lg.Component("tool").Warn(msg)
	}))

	var mem agent.Memory
	if boolOr(cfg.Agent.Memory, true) {
		maxPerScope := intOr(cfg.Agent.MemoryMax, 64)
		if path := strings.TrimSpace(cfg.Agent.MemoryFile); path != "" {
			// 落盘：复用 F-38 的 JSONL 存储；裁剪用高水位批量进行，
			// 避免每次写入都缩短记忆段（那会让记忆块每轮都变，白白失效缓存）。
			file := history.NewFile(path, maxPerScope).WithTrimmer(history.HighWater{
				Max: maxPerScope,
				Low: maxPerScope * 3 / 4,
			})
			mem = agent.NewHistoryMemory(file)
			lg.Component("agent").Info("memory is persisted to disk",
				"path", path, "max_per_scope", maxPerScope)
		} else {
			mem = agent.NewMemoryStore(maxPerScope)
			lg.Component("agent").Warn("memory is in-process only; it will be lost on restart (set agent.memory_file to persist)")
		}
	}

	// 内置工具：先全量注册再按配置裁剪，这样顺序始终等于内置顺序（前缀缓存需要稳定）。
	deps := builtin.Deps{Memory: mem, HTTP: httpx.Defaults(), Now: time.Now, History: hist}
	if err := builtin.Register(registry, deps); err != nil {
		return nil, fmt.Errorf("register builtin tools: %w", err)
	}
	if len(cfg.Agent.Tools) > 0 {
		keep := map[string]bool{}
		for _, n := range cfg.Agent.Tools {
			keep[n] = true
		}
		for _, n := range registry.Names() {
			if !keep[n] {
				registry.Remove(n)
			}
		}
	}

	if boolOr(cfg.Agent.VirtualActions, true) {
		if err := agent.RegisterVirtual(registry, mem); err != nil {
			return nil, fmt.Errorf("register virtual actions: %w", err)
		}
	}

	react := &agent.ReactAgent{
		LLM:             model,
		Tools:           registry,
		SystemPrompt:    sysPrompt,
		MaxIterations:   intOr(cfg.Agent.MaxIterations, agent.DefaultMaxIterations),
		StepTimeout:     durationOr(cfg.Agent.StepTimeout, agent.DefaultStepTimeout),
		Protocol:        agent.Protocol(strings.ToLower(strings.TrimSpace(cfg.Agent.Protocol))),
		Memory:          mem,
		ApprovalTimeout: durationOr(cfg.Agent.ApprovalTimeout, agent.DefaultApprovalTimeout),
		Warn:            func(msg string) { lg.Component("agent").Warn(msg) },
	}

	if cfg.Agent.ApprovalEnabled {
		gate := agent.NewTableGate()
		for toolName, roles := range cfg.Agent.Allow {
			for _, r := range roles {
				gate.Set(toolName, agent.Role(r), agent.VerdictAllow)
			}
		}
		react.Gate = gate
		// M2 尚未接入交互式审批通道：未放行的调用会被明确拒绝并回灌原因，
		// 而不是静默放行——这是 fail-closed 的正确表现。
		react.Approver = agent.ApproverFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, error) {
			return agent.Decision{}, fmt.Errorf("该部署未配置人工审批通道")
		})
		lg.Component("agent").Warn("tool approval is enabled but no interactive approver is wired; non-allowed calls will be denied")
	}

	lg.Component("agent").Info("react agent enabled",
		"tools", registry.Names(),
		"max_iterations", react.MaxIterations,
		"protocol", string(react.Protocol),
		"step_timeout", react.StepTimeout.String(),
		"memory", mem != nil,
		"memory_file", strings.TrimSpace(cfg.Agent.MemoryFile),
		"history_file", strings.TrimSpace(cfg.History.File),
		"approval", cfg.Agent.ApprovalEnabled)
	return react, nil
}

// sendShape 描述回复的发送形态（是否按空行拆分、连发间隔、最多几条）。
type sendShape struct {
	splitOnBlank bool
	delay        time.Duration
	maxSegments  int
}

// sendShapeOf 从配置读取发送形态，缺省值与 config.Default 保持一致。
func sendShapeOf(cfg *config.Config) sendShape {
	shape := sendShape{splitOnBlank: true, delay: 400 * time.Millisecond, maxSegments: outbound.DefaultMaxSegments}
	if cfg.Behavior.SplitOnBlankLine != nil {
		shape.splitOnBlank = *cfg.Behavior.SplitOnBlankLine
	}
	if cfg.Behavior.SplitDelay != nil && cfg.Behavior.SplitDelay.D >= 0 {
		shape.delay = cfg.Behavior.SplitDelay.D
	}
	if cfg.Behavior.MaxSegments != nil && *cfg.Behavior.MaxSegments > 0 {
		shape.maxSegments = *cfg.Behavior.MaxSegments
	}
	return shape
}

// replyJob 是一次待回复的消息。
type replyJob struct {
	key     session.Key
	groupID int64
	userID  int64
	text    string
	traceID string
	role    agent.Role
}

// namedComponent 把裸函数适配成 bot.Component。
type namedComponent struct {
	name  string
	close func(ctx context.Context) error
}

func (c namedComponent) Name() string { return c.name }

func (c namedComponent) Close(ctx context.Context) error { return c.close(ctx) }

func serve(cfg *config.Config, stderr io.Writer) int {
	timeout := shutdownTimeout(cfg)

	lg := observe.New(observe.Options{
		Level:        cfg.Log.Level,
		Format:       cfg.Log.Format,
		Components:   cfg.Log.Components,
		DebugContent: cfg.Log.DebugContent,
		QueueSize:    queueSize(cfg),
		Writer:       os.Stdout,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	}()
	lifecycle := lg.Component("lifecycle")

	model, err := buildLLM(cfg, lg)
	if err != nil {
		lifecycle.Error("cannot build llm", "error", err)
		return 1
	}

	// 缓存优先（二）：历史裁剪交给存储层，且用高水位批量裁剪。
	// 若由装配层每轮裁剪，前缀会逐轮变化，前缀缓存永远无法命中。
	shape := sendShapeOf(cfg)
	// 呈现窗口：每轮真正回灌给模型的条数（一轮 ≈ user + assistant 两条）。
	histItems := historyTurns(cfg) * 2
	// **存储**保留量远大于呈现窗口：否则 recall_history 只能返回已经出现在
	// 提示词里的内容，等于摆设。两者分开是让那个工具真正有用的前提。
	retention := intOr(cfg.History.Retention, 400)
	if retention < histItems {
		retention = histItems
	}
	var hist history.History
	if path := strings.TrimSpace(cfg.History.File); path != "" {
		hist = history.NewFile(path, retention).WithTrimmer(history.HighWater{
			Max: retention,
			Low: retention * 3 / 4,
		})
		lg.Component("session").Info("conversation history is persisted to disk",
			"path", path, "retention", retention, "prompt_window", histItems)
	} else {
		hist = history.NewMemory(retention).WithTrimmer(history.HighWater{
			Max: retention,
			Low: retention * 3 / 4,
		})
		lg.Component("session").Warn("conversation history is in-process only; it will be lost on restart (set history.file to persist)",
			"retention", retention, "prompt_window", histItems)
	}
	sessions := session.New(
		session.WithHistory(hist),
		session.WithTTL(session.DefaultTTL),
		session.WithMax(session.DefaultMax),
	)

	// 缓存优先（一）：不可变前缀在启动时固定一次，所有会话共享同一段前缀，
	// 因此公共前缀检测能让不同会话也命中同一块缓存。
	// MaxHistory=0：装配层不再二次裁剪，裁剪权只归存储层。
	sysPrompt, err := systemPrompt(cfg)
	if err != nil {
		lifecycle.Error("cannot load system prompt", "error", err)
		return 1
	}
	// 呈现窗口交给装配层：窗口按批量滑动（见 conversation.trimHistory），
	// 因此存储可以留得更多而不打碎前缀缓存。
	asm := conversation.New(conversation.Options{System: sysPrompt, MaxHistory: histItems})
	lg.Component("llm").Info("cache-first layout pinned",
		"prefix_hash", asm.PrefixHash(), "prefix_runes", len([]rune(sysPrompt)),
		"history_items", histItems, "trim_high_water", histItems, "trim_low_water", histItems*3/4)

	// Agent：启用时走 ReAct（带工具），否则是直连 LLM。调用方对两条路径同形。
	brain, err := buildAgent(cfg, model, sysPrompt, hist, lg)
	if err != nil {
		lifecycle.Error("cannot build agent", "error", err)
		return 1
	}

	routes := router.NewRouter(router.WithWarnFunc(func(msg string) {
		lg.Component("router").Warn(msg)
	}))
	engine := router.NewEngine(routes,
		router.WithPanicHandler(func(phase string, recovered any, stack []byte) {
			lg.Component("router").Error("recovered panic", "phase", phase, "panic", fmt.Sprint(recovered), "stack", string(stack))
		}),
		router.WithRejectHandler(func(c *router.Ctx, phase string) {
			lg.Component("router").Debug("route rejected", "phase", phase)
		}),
	)

	auth := transport.NewAuth(
		stringOr(cfg.Transport.AccessToken, ""),
		stringOr(cfg.Transport.SignatureSecret, ""),
		cfg.Transport.IPAllowlist,
	)
	if err := auth.Validate(); err != nil {
		lifecycle.Error("invalid transport auth config", "error", err)
		return 1
	}
	ws := transport.NewWSClient(cfg.Transport.URL, auth, transport.WithFrameErrorHook(func(raw []byte, err error) {
		// 协议不匹配必须看得见：静默丢弃会让机器人表现为完全没反应。
		lg.Component("transport").Error("cannot parse frame", "error", err, "bytes", len(raw))
	}))

	chain := outbound.New(
		outbound.WithMaxLength(2000),
		outbound.WithPanicHook(func(name string, recovered any) {
			lg.Component("outbound").Error("filter panicked", "filter", name, "panic", fmt.Sprint(recovered))
		}),
	)
	sender := outbound.NewSender(ws, chain, outbound.WithAudit(func(rec outbound.AuditRecord) {
		lg.Component("outbound").Info("outbound",
			"group_id", rec.GroupID, "user_id", rec.UserID,
			"dropped", rec.Dropped, "reason", rec.Reason, "runes", len([]rune(rec.Filtered)))
	}))

	listenCtx, stopListen := context.WithCancel(context.Background())
	jobs := make(chan replyJob, 256)
	var inflight sync.WaitGroup

	app := bot.New(
		bot.WithShutdownTimeout(timeout),
		bot.WithInflightTimeout(timeout),
		bot.WithIntakeStop(func(ctx context.Context) error {
			stopListen()
			return nil
		}),
		bot.WithInflightWait(func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { inflight.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	)

	// 回复策略来自配置：私聊 always/never，群聊 always/on_mention/never（见 behavior）。
	routes.OnMessage(replyRule(cfg)).
		Named("reply").
		Priority(router.PriorityNormal).
		Handle(func(c *router.Ctx) {
			// 用 Summary 而不是 PlainText：纯表情/纯图片消息也要能被回复，
			// 否则它们会被静默丢弃（既没回复也没日志）。
			text := strings.TrimSpace(c.Event.Message.Summary())
			if text == "" {
				return
			}
			inflight.Add(1)
			select {
			case jobs <- replyJob{
				key:     sessions.KeyFor(c.Event.SelfID, c.Event.GroupID, c.Event.UserID),
				groupID: c.Event.GroupID,
				userID:  c.Event.UserID,
				text:    text,
				traceID: observe.TraceID(c),
				role:    agentRole(c.Event),
			}:
			default:
				inflight.Done()
				lifecycle.Warn("reply queue is full; dropping message", "user_id", c.Event.UserID)
			}
		})

	for i := 0; i < replyWorkers; i++ {
		app.Go("reply-worker", func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-jobs:
					func() {
						defer inflight.Done()
						defer func() {
							if rec := recover(); rec != nil {
								lg.Component("reply").Error("worker panic", "panic", fmt.Sprint(rec))
							}
						}()
						handleReply(ctx, lg, brain, sender, sessions, asm, timeout, shape, j)
					}()
				}
			}
		})
	}

	sink := func(raw []byte, caller transport.Caller) {
		ev := event.NewEvent(raw)
		tlog := lg.Component("transport")
		if ev.Kind == "" {
			tlog.Debug("ignored frame without post_type", "bytes", len(raw))
			return
		}
		if ev.DecodeWarning != "" {
			// 解析降级必须可见：它是"看起来没反应"的第一手线索。
			tlog.Warn("event decoded with warnings",
				"warning", ev.DecodeWarning, "segments", segmentTypes(ev.Message))
		}
		tlog.Debug("event received",
			"kind", string(ev.Kind), "sub", ev.Sub, "self_id", ev.SelfID,
			"user_id", ev.UserID, "group_id", ev.GroupID, "message_id", ev.MessageID.String(),
			"segments", segmentTypes(ev.Message), "summary", ev.Message.Summary())
		if detail := segmentDetail(ev.Message); detail != "" {
			tlog.Debug("non-text segment fields", "detail", detail)
		}
		// F-15：会话级临时路由优先于常规路由。命中即消费，不再进入常规路由——
		// 否则 Await 等待的那条消息会同时被常规路由处理一遍。
		if sessions.Temp().Offer(sessions.KeyFor(ev.SelfID, ev.GroupID, ev.UserID), ev) {
			tlog.Debug("event consumed by a temporary route",
				"self_id", ev.SelfID, "user_id", ev.UserID, "group_id", ev.GroupID)
			return
		}

		ectx := observe.WithTraceID(listenCtx, traceID(ev))
		engine.Dispatch(ectx, ev, caller)
	}

	app.Go("ws-session", func(ctx context.Context) {
		if err := ws.Connect(ctx); err != nil {
			lg.Component("transport").Error("connect failed", "url", cfg.Transport.URL, "error", err)
			return
		}
		go func() {
			resp, err := ws.Call(ctx, transport.Request{Action: "get_login_info"})
			if err != nil {
				lg.Component("transport").Warn("get_login_info failed", "error", err)
				return
			}
			var info struct {
				UserID   int64  `json:"user_id"`
				Nickname string `json:"nickname"`
			}
			_ = json.Unmarshal(resp.Data, &info)
			lg.Component("transport").Info("logged in",
				"self_id", info.UserID, "nickname", info.Nickname, "configured_self_id", configuredSelfID(cfg))
		}()
		if err := ws.Listen(listenCtx, sink); err != nil && listenCtx.Err() == nil {
			lg.Component("transport").Error("read loop stopped", "error", err)
		}
	})

	if err := app.Register(bot.PhaseSession, sessions); err != nil {
		lifecycle.Error("register session manager", "error", err)
		return 1
	}
	if err := app.Register(bot.PhaseTransport, namedComponent{name: "ws-client", close: ws.Close}); err != nil {
		lifecycle.Error("register transport", "error", err)
		return 1
	}
	if err := app.Register(bot.PhaseStorage, namedComponent{name: "logger", close: lg.Close}); err != nil {
		lifecycle.Error("register logger", "error", err)
		return 1
	}

	app.MarkRunning()
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	if provider == "" {
		provider = "openai"
	}
	lifecycle.Info("agentbot started",
		"transport", cfg.Transport.Mode,
		"llm_provider", provider,
		"capabilities", []string{
			"event-kernel", "transport-wsclient", "transport-auth",
			"router-snapshot-match", "rule-handler-separation", "engine-hooks",
			"session-manager", "history-memory", "llm-interface", "llm-retry",
			"outbound-filter-chain", "graceful-shutdown",
		},
	)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	<-sig
	lifecycle.Info("shutdown signal received", "timeout", timeout.String())

	go func() {
		<-sig
		_, _ = fmt.Fprintln(stderr, "second signal received: forcing exit")
		os.Exit(1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		lifecycle.Error("shutdown incomplete", "error", err)
		return 1
	}
	lifecycle.Info("shutdown complete")
	return 0
}

// handleReply 调用模型并把回复经唯一出口发出。
//
// 这里落实"缓存优先"：消息序列固定为 [不可变前缀] + [只追加历史] + [当前输入]，
// 并记录 DeepSeek 返回的缓存命中计量，让命中率可观测、可回归。
func handleReply(ctx context.Context, lg *observe.Logger, brain agent.Agent, sender *outbound.Sender,
	sessions *session.Manager, asm *conversation.Assembler, timeout time.Duration, shape sendShape, j replyJob) {
	rlog := lg.Component("reply")
	callCtx, cancel := context.WithTimeout(observe.WithTraceID(ctx, j.traceID), timeout)
	defer cancel()

	sess := sessions.GetOrCreate(j.key)
	histKey := j.key.String()
	items, err := sess.Hist.Messages(callCtx, histKey)
	if err != nil {
		// 读不到历史不该拒绝服务：退化成单轮，但要留下痕迹。
		rlog.Warn("cannot read history; falling back to a single turn", "error", err)
	}

	// 两条路径（ReAct / 直连）在调用方看完全同形。
	// 走 ReAct 时，记忆的注入位置由 Agent 按 ADR-0002 处理（system 之后、历史之前）。
	out, runErr := brain.Run(callCtx, agent.Input{
		Query:      j.text,
		History:    conversation.ToMessages(items),
		SessionKey: j.key,
		Role:       j.role,
	})

	// F-40：模型主动结束本轮。这**不是失败**，但也不发任何消息。
	if errors.Is(runErr, agent.ErrEndOfTurn) {
		if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindUser, Content: j.text}); err != nil {
			rlog.Warn("cannot append user turn", "error", err)
		}
		rlog.Info("turn ended by end_action", "group_id", j.groupID, "user_id", j.userID)
		return
	}
	if runErr != nil {
		rlog.Error("agent run failed", "error", runErr, "steps", len(out.Steps))
		return
	}

	reply := strings.TrimSpace(out.Text)
	if reply == "" {
		rlog.Warn("agent returned empty text", "steps", len(out.Steps), "finish_reason", out.FinishReason)
		return
	}

	// 只追加、绝不改写：这是下一轮还能命中前缀缓存的前提。
	if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindUser, Content: j.text}); err != nil {
		rlog.Warn("cannot append user turn", "error", err)
	}
	if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindAssistant, Content: reply}); err != nil {
		rlog.Warn("cannot append assistant turn", "error", err)
	}

	rlog.Info("llm call",
		"prefix_hash", asm.PrefixHash(),
		"steps", len(out.Steps),
		"tool_calls", len(out.ToolCalls),
		"tools", toolNames(out.ToolCalls),
		"prompt_tokens", out.Usage.PromptTokens,
		"completion_tokens", out.Usage.CompletionTokens,
		"reasoning_tokens", out.Usage.ReasoningTokens,
		"cache_hit_tokens", out.Usage.PromptCacheHitTokens,
		"cache_miss_tokens", out.Usage.PromptCacheMissTokens,
		"cache_hit_ratio", fmt.Sprintf("%.1f%%", out.Usage.CacheHitRatio()*100),
	)

	target := outbound.PrivateTarget(j.userID)
	if j.groupID != 0 {
		target = outbound.GroupTarget(j.groupID)
	}

	// 真人是一条一条发的：按空行拆成多条分别发送，而不是一整块砸过去。
	parts := []string{reply}
	if shape.splitOnBlank {
		parts = outbound.SplitParagraphs(reply, shape.maxSegments)
	}
	if len(parts) == 0 {
		rlog.Warn("reply became empty after splitting")
		return
	}
	sent, err := sender.SendMany(callCtx, target, parts, shape.delay)
	if err != nil {
		rlog.Error("send failed", "error", err, "sent", sent, "segments", len(parts))
		return
	}
	// 每条讯息都带上它自己的缓存命中率：这是"缓存优先"是否生效的唯一客观指标。
	rlog.Info("replied", "group_id", j.groupID, "user_id", j.userID,
		"runes", len([]rune(reply)), "segments", len(parts),
		"cache_hit_ratio", fmt.Sprintf("%.1f%%", out.Usage.CacheHitRatio()*100),
		"cache_hit_tokens", out.Usage.PromptCacheHitTokens,
		"cache_miss_tokens", out.Usage.PromptCacheMissTokens)
}

// toolNames 汇总本轮用到的工具名，便于在日志里核对"到底调了什么"。
func toolNames(calls []llm.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Name)
	}
	return strings.Join(names, ",")
}

// segmentDetail 渲染非文本段的全部字段，用于确认平台真实载荷。
//
// 例：image{file=a.jpg,file_size=12345,sub_type=1} face{id=4}
func segmentDetail(m event.Message) string {
	parts := make([]string, 0, len(m))
	for _, seg := range m {
		if seg.Type == event.TypeText {
			continue
		}
		keys := make([]string, 0, len(seg.Data))
		for k := range seg.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kv := make([]string, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, k+"="+seg.Data[k])
		}
		parts = append(parts, seg.Type+"{"+strings.Join(kv, ",")+"}")
	}
	return strings.Join(parts, " ")
}

// segmentTypes 把消息段类型拼成 "text+face+image" 形式，便于在日志里看清平台真实载荷。
func segmentTypes(m event.Message) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for _, seg := range m {
		parts = append(parts, seg.Type)
	}
	return strings.Join(parts, "+")
}

// traceID 为一次事件生成贯穿日志的标识。
func traceID(ev *event.Event) string {
	return fmt.Sprintf("%d-%d-%s", ev.SelfID, ev.Time.Unix(), ev.MessageID.String())
}

// runSelfTest 只做连通性验证：连接 → get_login_info → 发一条消息给指定用户 → 退出。
func runSelfTest(cfg *config.Config, target int64, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	auth := transport.NewAuth(stringOr(cfg.Transport.AccessToken, ""), "", cfg.Transport.IPAllowlist)
	ws := transport.NewWSClient(cfg.Transport.URL, auth)
	defer func() { _ = ws.Close(context.Background()) }()

	if err := ws.Connect(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "connect %s failed: %v\n", cfg.Transport.URL, err)
		return 1
	}
	sender := outbound.NewSender(ws, outbound.New())
	go func() { _ = ws.Listen(ctx, func([]byte, transport.Caller) {}) }()

	resp, err := ws.Call(ctx, transport.Request{Action: "get_login_info"})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "get_login_info failed: %v\n", err)
		return 1
	}
	var info struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	_ = json.Unmarshal(resp.Data, &info)
	_, _ = fmt.Printf("connected: %s\nlogged in as: %d (%s)\n", cfg.Transport.URL, info.UserID, info.Nickname)

	id, err := sender.Send(ctx, outbound.PrivateTarget(target), event.Message{event.Text("AgentBot 自检：链路正常（可忽略）。")})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "send failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Printf("sent self-test message to %d, message_id=%s\n", target, id.String())
	return 0
}
