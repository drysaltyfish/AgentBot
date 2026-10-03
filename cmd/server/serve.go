package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

func serve(cfg *config.Config, stderr io.Writer) int {
	timeout := cfg.Shutdown.EffectiveTimeout()

	lg := observe.New(observe.Options{
		Level:        cfg.Log.Level,
		Format:       cfg.Log.Format,
		Components:   cfg.Log.Components,
		DebugContent: cfg.Log.DebugContent,
		QueueSize:    cfg.Log.EffectiveQueueSize(),
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
	// F-83：持久层。打不开就启动失败——不得静默降级为内存（那会悄悄丢数据）。
	// 打开与迁移给一个独立预算：卡住时要在启动阶段暴露，而不是拖到第一条消息。
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.Open(openCtx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: cfg.Store.BusyTimeoutOr(store.DefaultBusyTimeout),
	})
	cancelOpen()
	if err != nil {
		lifecycle.Error("cannot open the persistence store", "error", err, "path", cfg.Store.Path)
		return 1
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			lifecycle.Warn("cannot close the persistence store", "error", cerr)
		}
	}()
	lg.Component("store").Info("persistence store is ready",
		"path", st.Path(), "schema_version", store.SchemaVersion)

	// F-89：提示词快照是**环形保留**的，启动时裁一次即可保证有界。
	{
		pruneCtx, cancelPrune := context.WithTimeout(context.Background(), 15*time.Second)
		if n, perr := st.PrunePromptSnapshots(pruneCtx, promptSnapshotKeep); perr != nil {
			lg.Component("store").Warn("cannot prune prompt snapshots", "error", perr)
		} else if n > 0 {
			lg.Component("store").Info("pruned old prompt snapshots", "count", n, "keep_per_session", promptSnapshotKeep)
		}
		cancelPrune()
	}

	histItems := cfg.LLM.EffectiveHistoryTurns() * 2
	// **存储**保留量远大于呈现窗口：否则 recall_history 只能返回已经出现在
	// 提示词里的内容，等于摆设。两者分开是让那个工具真正有用的前提。
	retention := cfg.History.EffectiveRetention()
	if retention < histItems {
		retention = histItems
	}
	// F-84：历史落在持久层。JSONL 实现保留下来只用于导入与故障排查。
	sqliteHist := history.NewSQLite(st, retention).WithTrimmer(history.HighWater{
		Max: retention,
		Low: retention * 3 / 4,
	})
	var hist history.History = sqliteHist
	lg.Component("session").Info("conversation history is stored in the database",
		"retention", retention, "prompt_window", histItems)

	// 一次性迁移：库为空且存在旧 JSONL 时导入。导入本身幂等，因此这里只在空库时触发，
	// 避免每次启动都白读一遍文件。
	if legacy := strings.TrimSpace(cfg.History.File); legacy != "" {
		// 迁移是一次性的启动动作，给它独立预算，不占用请求 ctx。
		migCtx, cancelMig := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelMig()
		total, err := st.TotalMessageCount(migCtx)
		switch {
		case err != nil:
			lifecycle.Warn("cannot check message count; skipping legacy import", "error", err)
		case total > 0:
			lg.Component("session").Info("database already has messages; skipping legacy JSONL import",
				"messages", total, "legacy_path", legacy)
		default:
			imported, skipped, ierr := sqliteHist.ImportJSONL(migCtx, legacy)
			switch {
			case errors.Is(ierr, history.ErrImportSourceMissing):
				lg.Component("session").Info("no legacy history file to import", "path", legacy)
			case ierr != nil:
				lifecycle.Warn("legacy history import failed; starting with an empty history",
					"error", ierr, "path", legacy)
			default:
				lg.Component("session").Info("legacy JSONL history imported",
					"path", legacy, "imported", imported, "skipped", skipped)
			}
		}
	}
	sessions := session.New(
		session.WithHistory(hist),
		session.WithTTL(session.DefaultTTL),
		session.WithMax(session.DefaultMax),
	)
	// F-86：把等待落盘，重启后至少能通知原会话，而不是让用户一直干等。
	sessions.Temp().WithPendingStore(pendingStoreAdapter{st: st}, func(msg string) {
		lg.Component("session").Warn(msg)
	})

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
	// 让模型自己判断该记什么：把长期记忆指令并入系统提示词末尾。
	// 追加在末尾且内容固定，因此不可变前缀的完整性不受影响。
	if cfg.Agent.ProactiveMemory.EffectiveEnabled() {
		sysPrompt = agent.ComposeSystemPrompt(sysPrompt,
			agent.ProactiveMemoryInstruction(cfg.Agent.ProactiveMemory.Instruction))
	}
	// 自我介绍：模型必须知道自己是哪个号，否则认不出别人在 @ 它。
	if ident := agent.SelfIdentity(cfg.Transport.EffectiveSelfID(), ""); ident != "" {
		sysPrompt = agent.ComposeSystemPrompt(sysPrompt, ident)
	}
	// 工具使用提示：被引用内容只有一句，很久远时缺上下文——
	// 告诉模型它可以用 recall_history 回溯，否则它不会想到这个手段。
	if cfg.Agent.Enabled && cfg.Agent.ToolHint.EffectiveEnabled() {
		sysPrompt = agent.ComposeSystemPrompt(sysPrompt,
			agent.ToolUsageInstruction(cfg.Agent.ToolHint.Instruction))
	}
	asm := conversation.New(conversation.Options{
		System:     sysPrompt,
		MaxHistory: histItems,
		// 环境消息（群里没被 @ 的）按 token 预算压缩，不跟对话争窗口。
		AmbientTokenBudget: cfg.LLM.AmbientTokenBudgetOr(conversation.DefaultAmbientTokenBudget),
		AmbientMaxChars:    cfg.LLM.AmbientMaxCharsOr(conversation.DefaultAmbientMaxChars),
	})
	lg.Component("llm").Info("cache-first layout pinned",
		"prefix_hash", asm.PrefixHash(), "prefix_runes", len([]rune(sysPrompt)),
		"history_items", histItems, "trim_high_water", histItems, "trim_low_water", histItems*3/4)

	// Agent：启用时走 ReAct（带工具），否则是直连 LLM。调用方对两条路径同形。
	// 平台 API 通道：ws 稍后才创建，因此用迟到绑定的盒子。
	apiCaller := &callerBox{}
	brain, mem, err := buildAgent(cfg, model, asm, hist, st, apiCaller, lg)
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

	apiCaller.set(ws)

	sender := outbound.NewSender(ws, chain, outbound.WithAudit(func(rec outbound.AuditRecord) {
		lg.Component("outbound").Info("outbound",
			"group_id", rec.GroupID, "user_id", rec.UserID,
			"dropped", rec.Dropped, "reason", rec.Reason, "runes", len([]rune(rec.Filtered)))
	}))
	// F-86：恢复残留的在途记录（通知原会话；已过期的作废）。
	{
		recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 30*time.Second)
		recoverPending(recoverCtx, st, sender, lg)
		cancelRecover()
	}

	listenCtx, stopListen := context.WithCancel(context.Background())
	jobs := make(chan reply.Job, 256)
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

	// F-48 的规则触发：用户说"记住：xxx"时自动写入记忆，不依赖模型是否调工具。
	var autoMem *agent.MemoryCommand
	if cfg.Agent.AutoMemory.Enabled {
		autoMem = agent.NewMemoryCommand(cfg.Agent.AutoMemory.Triggers)
		lg.Component("agent").Info("explicit memory commands are auto-saved",
			"triggers", autoMem.Triggers())
	} else {
		lg.Component("agent").Info("keyword-triggered memory is disabled")
	}
	lg.Component("agent").Info("proactive memory (model decides)",
		"enabled", cfg.Agent.ProactiveMemory.EffectiveEnabled())

	// 价格未配置时成本恒为 0，但用量（token/请求数）仍然照记——
	// "花了多少 token"与"花了多少钱"是两件事，前者不依赖价格表。
	price := llm.Price{
		Version:            cfg.LLM.Pricing.Version,
		InputPerMillion:    floatOr(cfg.LLM.Pricing.InputPerMillion, 0),
		OutputPerMillion:   floatOr(cfg.LLM.Pricing.OutputPerMillion, 0),
		CacheHitPerMillion: floatOr(cfg.LLM.Pricing.CacheHitPerMillion, 0),
	}
	if price.Enabled() {
		lg.Component("llm").Info("cost tracking is enabled", "pricing_version", price.Version,
			"input_per_million", price.InputPerMillion, "cache_hit_per_million", price.CacheHitPerMillion,
			"output_per_million", price.OutputPerMillion)
	} else {
		lg.Component("llm").Info("pricing is not configured; cost stays 0 (token usage is still recorded)")
	}

	pipeline := reply.New(reply.Deps{
		Brain: brain, Sender: sender, Sessions: sessions, Assembler: asm,
		Memory: mem, AutoMem: autoMem, Timeout: timeout, Shape: shape,
		Store: st, Price: price, Log: lg,
	})

	// 回复策略是**规则**，不是 handler 里的分支：路由层就能回答"什么时候回复"。
	// 命中即回复（Block 阻止下面的只记录路由重复入队）；
	// 群里的环境消息不回复，但仍然入队记录——它们是模型理解"刚才在聊什么"的依据。
	enqueue := func(c *router.Ctx, shouldReply bool) {
		// 用 Summary 而不是 PlainText：纯表情/纯图片消息也要能被回复，
		// 否则它们会被静默丢弃（既没回复也没日志）。
		text := strings.TrimSpace(c.Event.Message.Summary())
		if text == "" {
			return
		}
		inflight.Add(1)
		select {
		case jobs <- reply.Job{
			Key:         sessions.KeyFor(c.Event.SelfID, c.Event.GroupID, c.Event.UserID),
			GroupID:     c.Event.GroupID,
			UserID:      c.Event.UserID,
			Text:        text,
			TraceID:     observe.TraceID(c),
			Role:        agentRole(c.Event),
			ShouldReply: shouldReply,
			SpeakerID:   groupScopedUserID(c.Event.UserID, c.Event.GroupID),
			SpeakerName: speakerDisplayName(c.Event.Sender, c.Event.GroupID),
			Message:     c.Event.Message,
			Caller:      c.Caller(),
		}:
		default:
			inflight.Done()
			lifecycle.Warn("reply queue is full; dropping message", "user_id", c.Event.UserID)
		}
	}

	routes.OnMessage(replyRule(cfg)).
		Named("reply").
		Priority(router.PriorityEarly).
		Block(true).
		Handle(func(c *router.Ctx) { enqueue(c, true) })

	routes.OnMessage(router.Always()).
		Named("record").
		Priority(router.PriorityLate).
		Handle(func(c *router.Ctx) { enqueue(c, false) })

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
						pipeline.Handle(ctx, j)
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
		// 注意：**不能在 sink 里调用平台 API**。Listen 读完帧后是同步调用 sink 的，
		// 而 API 的响应也只能由同一个读循环读回来——在这里 Call 必然死锁。
		// 引用解析因此放在回复 worker 里做（见 internal/reply）。
		// F-15：会话级临时路由优先于常规路由。命中即消费，不再进入常规路由——
		// 否则 Await 等待的那条消息会同时被常规路由处理一遍。
		//nolint:contextcheck // Offer 只在过期清理时做后台收尾，事件循环本身没有请求 ctx
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
				"self_id", info.UserID, "nickname", info.Nickname, "configured_self_id", cfg.Transport.EffectiveSelfID())
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
