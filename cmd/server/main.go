// AgentBot 的组合根：读配置 → 装配 → 启动 → 优雅关闭（FEATURES.md F-25 / F-70）。
package main

import (
	"context"
	"encoding/json"
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

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/retry"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
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

// buildLLM 按配置选择模型实现。echo 是联调用的假实现。
func buildLLM(cfg *config.Config, lg *observe.Logger) (llm.LLM, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.LLM.Provider)) {
	case "echo":
		lg.Component("llm").Warn("using the echo provider: replies are a fixed template, not a real model")
		return llm.NewEcho(""), nil
	case "", "openai":
		client := httpx.NewClient(httpx.Config{
			Timeout:      llmTimeout(cfg),
			MaxBytes:     httpx.Defaults().MaxBytes,
			MaxRedirects: 3,
			// 本地/内网 provider（如 Ollama）需要显式放行；默认按 F-59 拒绝私网。
			AllowPrivate: false,
		})
		base := llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL: cfg.LLM.BaseURL,
			APIKey:  stringOr(cfg.LLM.APIKey, ""),
			Model:   cfg.LLM.Model,
			Client:  client,
		})
		return llm.NewRetryLLM(base, retry.Default()), nil
	default:
		return nil, fmt.Errorf("unsupported llm.provider %q", cfg.LLM.Provider)
	}
}

func llmTimeout(cfg *config.Config) time.Duration {
	if cfg.LLM.Timeout != nil && cfg.LLM.Timeout.D > 0 {
		return cfg.LLM.Timeout.D
	}
	return 30 * time.Second
}

// replyJob 是一次待回复的消息。
type replyJob struct {
	groupID int64
	userID  int64
	text    string
	traceID string
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

	sessions := session.New(
		session.WithHistory(history.NewMemory(50)),
		session.WithTTL(session.DefaultTTL),
		session.WithMax(session.DefaultMax),
	)

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

	// 路由：私聊一律回复；群里只在 @ 机器人时回复，避免刷屏；绝不回复自己。
	routes.OnMessage(
		func(c *router.Ctx) bool {
			return c.Event != nil && c.Event.UserID != 0 && c.Event.UserID != c.Event.SelfID
		},
		router.Or(router.OnlyPrivate(), router.AtMe()),
	).
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
			case jobs <- replyJob{groupID: c.Event.GroupID, userID: c.Event.UserID, text: text, traceID: observe.TraceID(c)}:
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
						handleReply(ctx, lg, model, sender, timeout, j)
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
func handleReply(ctx context.Context, lg *observe.Logger, model llm.LLM, sender *outbound.Sender, timeout time.Duration, j replyJob) {
	rlog := lg.Component("reply")
	callCtx, cancel := context.WithTimeout(observe.WithTraceID(ctx, j.traceID), timeout)
	defer cancel()

	resp, err := model.Chat(callCtx, &llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: j.text}}})
	if err != nil {
		rlog.Error("llm call failed", "error", err)
		return
	}
	reply := strings.TrimSpace(resp.Content)
	if reply == "" {
		rlog.Warn("llm returned empty content")
		return
	}

	target := outbound.PrivateTarget(j.userID)
	if j.groupID != 0 {
		target = outbound.GroupTarget(j.groupID)
	}
	if _, err := sender.Send(callCtx, target, event.Message{event.Text(reply)}); err != nil {
		rlog.Error("send failed", "error", err)
		return
	}
	rlog.Info("replied", "group_id", j.groupID, "user_id", j.userID, "runes", len([]rune(reply)))
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
