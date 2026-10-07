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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/drysaltyfish/agentbot/internal/admin"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/backpressure"
	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/retry"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/scoped"
	"github.com/drysaltyfish/agentbot/internal/secrets"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

func serve(cfg *config.Config, configPath string, stderr io.Writer) int {
	timeout := cfg.Shutdown.EffectiveTimeout()

	lg := observe.New(observe.Options{
		Level:        cfg.Log.Level,
		Format:       cfg.Log.Format,
		Components:   cfg.Log.Components,
		DebugContent: cfg.Log.DebugContent,
		QueueSize:    cfg.Log.EffectiveQueueSize(),
		Writer:       os.Stdout,
		// F-61：全部日志（含 panic 堆栈）落盘前走同一个 Redactor。
		Redactor: secrets.Scrub,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	}()
	lifecycle := lg.Component("lifecycle")

	// F-60：审计不可关闭，先建好；后续每个关键动作都往它写。
	auditLog, auditCloser := buildAudit(cfg, lg)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := auditLog.Close(ctx); err != nil {
			lifecycle.Warn("cannot close audit log", "error", err)
		}
		if auditCloser != nil {
			_ = auditCloser.Close()
		}
	}()

	// F-68：全部指标在**同一处**定义；F-69 的探针复用它的注册表。
	// sessionMgr / replyQueue 在稍后赋值，这里只登记取值闭包。
	var (
		sessionMgr *session.Manager
		replyQueue chan reply.Job
		eventQueue *backpressure.Queue[eventJob]
		wsUp       atomic.Bool
	)
	catalog := metrics.NewCatalog(metrics.CatalogOptions{
		SessionsActive: func() float64 {
			if sessionMgr == nil {
				return 0
			}
			return float64(sessionMgr.Len())
		},
		QueueDepth: func() float64 {
			depth := len(replyQueue)
			if eventQueue != nil {
				depth += eventQueue.Depth()
			}
			return float64(depth)
		},
	})

	// ---- 阶段：模型 + 持久层 + 历史 ----
	// 打不开持久层、LLM 建不出来都会写进日志并返回错误，这里只负责 fail-fast。
	found, ferr := buildFoundation(cfg, lg, catalog)
	if ferr != nil {
		return 1
	}
	defer found.Close() // 逆序释放：成本台账 → 持久层
	model, st, hist := found.Model, found.Store, found.History
	costTracker, budget, histItems := found.Cost, found.Budget, found.PromptWindow

	// 缓存优先（二）：历史裁剪交给存储层，且用高水位批量裁剪（见 buildFoundation）。
	// 呈现窗口：每轮真正回灌给模型的条数（一轮 ≈ user + assistant 两条）。
	shape := sendShapeOf(cfg)

	sessions := session.New(
		session.WithHistory(hist),
		session.WithTTL(session.DefaultTTL),
		session.WithMax(session.DefaultMax),
	)
	sessionMgr = sessions
	// F-86：把等待落盘，重启后至少能通知原会话，而不是让用户一直干等。
	sessions.Temp().WithPendingStore(pendingStoreAdapter{st: st}, func(msg string) {
		lg.Component("session").Warn(msg)
	})

	// 缓存优先（一）：不可变前缀在启动时固定一次，所有会话共享同一段前缀，
	// 因此公共前缀检测能让不同会话也命中同一块缓存。
	// MaxHistory=0：装配层不再二次裁剪，裁剪权只归存储层。
	basePrompt, err := systemPrompt(cfg)
	if err != nil {
		lifecycle.Error("cannot load system prompt", "error", err)
		return 1
	}
	// F-33：不可变前缀由模板引擎渲染（prompt.dir 下的同名模板可覆盖内置版本）。
	// 段落顺序与分隔符从代码挪进模板，运维可以改；但内容仍**只随配置变化**，
	// 因此 F-65 的前缀缓存前提不变。校验在启动期完成，写错变量名不会等到线上。
	promptEngine, promptErr := buildPromptEngine(cfg, lg)
	if promptErr != nil {
		lifecycle.Error("cannot load prompt templates", "error", promptErr, "dir", cfg.Prompt.Dir)
		return 1
	}
	prefix := prefixData{SystemPrompt: basePrompt}
	// 呈现窗口交给装配层：窗口按批量滑动（见 conversation.trimHistory），
	// 因此存储可以留得更多而不打碎前缀缓存。
	// 让模型自己判断该记什么：把长期记忆指令并入系统提示词末尾。
	// 追加在末尾且内容固定，因此不可变前缀的完整性不受影响。
	if cfg.Agent.ProactiveMemory.EffectiveEnabled() {
		prefix.ProactiveMemory = agent.ProactiveMemoryInstruction(cfg.Agent.ProactiveMemory.Instruction)
	}
	// 自我介绍：模型必须知道自己是哪个号，否则认不出别人在 @ 它。
	prefix.Identity = agent.SelfIdentity(cfg.Transport.EffectiveSelfID(), "")
	// 工具使用提示：被引用内容只有一句，很久远时缺上下文——
	// 告诉模型它可以用 recall_history 回溯，否则它不会想到这个手段。
	if cfg.Agent.Enabled && cfg.Agent.ToolHint.EffectiveEnabled() {
		prefix.ToolHint = agent.ToolUsageInstruction(cfg.Agent.ToolHint.Instruction)
	}
	sysPrompt, err := renderSystemPrefix(promptEngine, prefix)
	if err != nil {
		lifecycle.Error("cannot render the system prompt", "error", err)
		return 1
	}
	// F-82：人格定义在启动期加载并校验——人格名写错要在启动时失败，
	// 而不是等第一个用户来聊天才发现。F-65 的半静态段取自这里。
	// F-53：权限表。它同时供提示词侧（半静态段渲染）与执行侧（API 硬拦截）使用。
	policyTable, policyErr := loadPolicy(cfg, lg)
	if policyErr != nil {
		lifecycle.Error("cannot load the policy table", "error", policyErr, "path", cfg.Policy.File)
		return 1
	}
	policyTables := newPolicyState(policyTable)
	// F-58 运维扩展：名单（路由层直接丢弃）与"QQ 号 → 角色"的显式指定。
	// 超管只有 access.roles.superuser 一个位置；旧的 moderation.super_users 出现即启动失败。
	accessCtl := buildAccessControls(cfg, lg)
	superUsers := superUsersFrom(accessCtl.Roles)

	personaReg, personaMgr, personaErr := buildPersonas(cfg, personaStoreAdapter{st: st}, lg)
	if personaErr != nil {
		lifecycle.Error("cannot load personas", "error", personaErr, "dir", cfg.Prompt.EffectivePersonasDir())
		return 1
	}
	asm := conversation.New(conversation.Options{
		System:     sysPrompt,
		MaxHistory: histItems,
		// 环境消息（群里没被 @ 的）按 token 预算压缩，不跟对话争窗口。
		AmbientTokenBudget: cfg.LLM.AmbientTokenBudgetOr(conversation.DefaultAmbientTokenBudget),
		AmbientMaxChars:    cfg.LLM.AmbientMaxCharsOr(conversation.DefaultAmbientMaxChars),
		// F-65 半静态段：会话人格设定。未配置人格时逐字节等于静态段。
		HalfStatic: policyPromptProvider(personaHalfStatic(personaReg, personaMgr, func(msg string) {
			lg.Component("persona").Warn(msg)
		}), policyTables, lg),
	})
	lg.Component("llm").Info("cache-first layout pinned",
		"prefix_hash", asm.PrefixHash(), "prefix_runes", len([]rune(sysPrompt)),
		"history_items", histItems, "trim_high_water", histItems, "trim_low_water", histItems*3/4)

	// Agent：启用时走 ReAct（带工具），否则是直连 LLM。调用方对两条路径同形。
	// 平台 API 通道：ws 稍后才创建，因此用迟到绑定的盒子。
	apiCaller := &callerBox{}
	brain, mem, err := buildAgent(cfg, model, asm, hist, st, apiCaller, lg, auditLog, catalog)
	if err != nil {
		lifecycle.Error("cannot build agent", "error", err)
		return 1
	}

	// F-65：/prompt-hash 的数据源——按调用者所在会话报告各段哈希。
	promptHash := personaPromptHash(asm, personaMgr, mem, cfg.Transport.EffectiveSelfID(), lg)

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
		router.WithObserver(routeMetrics{cat: catalog}),
	)

	// 名单判定挂在**最前面**：命中即丢弃，后面的审查、连接、会话都不会发生。
	// 先注册先执行——但它与其它 pre 钩子一起在下面集中注册（见"入站 pre 钩子"一段），
	// 因为"谁先谁后"是行为而不是实现细节，散在 180 行之外没人看得出顺序。

	auth, err := buildTransportAuth(cfg)
	if err != nil {
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

	// F-53：平台 API 的执行侧硬拦截，挂在**工具/模型调用**的那条通道上（apiCaller）。
	// 权限表回答的是"模型能用哪些平台动作"，而 apiCaller 正是工具唯一的平台出口，
	// 逐个调用点加判定一定会漏。
	apiCaller.set(transport.Chain(ws, policyMiddleware(policyTables, lg)))

	// 出站发送**不挂**权限表：回复投递是程序自己的行为，不是模型在选择动作。
	// 曾经把它也挂上，后果是群聊里连 send_group_msg 都被判为越权（内置表里 everyone
	// 只有私聊的 send_msg），表现为"机器人完全不回话"——一个只会发生在群聊、
	// 且报错藏在 outbound 日志里的静默故障。
	sender := outbound.NewSender(ws, chain, outbound.WithAudit(outboundAuditHook(catalog, lg)))
	// F-86：恢复残留的在途记录（通知原会话；已过期的作废）。
	{
		recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 30*time.Second)
		recoverPending(recoverCtx, st, sender, lg)
		cancelRecover()
	}

	listenCtx, stopListen := context.WithCancel(context.Background())
	jobs := make(chan reply.Job, 256)
	replyQueue = jobs
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

	// F-23：定期回收不再活跃的会话。只把 Close 接进 Shutdown 是不够的——
	// 长期运行的进程靠的是这条 ticker 来释放会话、临时路由与其绑定的状态。
	startSessionReclaimer(app, sessions, lg, session.DefaultReclaimEvery)

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

	// F-63：语义缓存（默认关闭）。接在回复链路而不是 LLM 装饰器上——
	// 只有这里同时拿得到会话键、人格指纹、出口过滤链与历史追加。
	semCache, serr := buildSemcache(cfg, chain, lg)
	if serr != nil {
		lifecycle.Error("cannot build the semantic cache", "error", serr)
		return 1
	}

	// F-64：流式增量发送（默认关闭）。只有 DirectAgent 会用到它——ReAct 每轮
	// 都在等完整的工具调用结果，边流边发会让用户先看到半截文本。
	streamFactory := buildStreamSplitterFactory(cfg, sender, lg)
	if streamFactory != nil && cfg.Agent.Enabled {
		lg.Component("stream").Warn("llm streaming is enabled but the ReAct agent cannot stream tool rounds; replies will be sent in one piece")
	}

	pipeline := reply.New(reply.Deps{
		Brain: brain, Sender: sender, Sessions: sessions, Assembler: asm,
		Memory: mem, AutoMem: autoMem, Timeout: timeout, Shape: shape,
		Store: st, Price: price, Log: lg,
		Audit: auditLog, Catalog: catalog,
		Semcache:          semCache,
		NewStreamSplitter: streamFactory,
		SemcacheFingerprint: func(ctx context.Context, key session.Key) string {
			if personaMgr == nil {
				return ""
			}
			fp, perr := personaMgr.Fingerprint(ctx, scoped.SessionRefForKey(key))
			if perr != nil {
				return ""
			}
			return fp
		},
	})

	// 回复策略是**规则**，不是 handler 里的分支：路由层就能回答"什么时候回复"。
	// 命中即回复（Block 阻止下面的只记录路由重复入队）；
	// 群里的环境消息不回复，但仍然入队记录——它们是模型理解"刚才在聊什么"的依据。
	// 构造与队满回滚的细节见 enqueue.go 与 enqueue_test.go。
	enqueue := jobEnqueuer{
		jobs:       jobs,
		sessions:   sessions,
		inflight:   &inflight,
		catalog:    catalog,
		accessCtl:  accessCtl,
		superUsers: superUsers,
		log:        lg,
	}.Enqueue

	// ---- 入站 pre 钩子：顺序即行为，按执行顺序集中注册 ----
	//
	// 引擎按**注册顺序**执行 pre 钩子，所以顺序本身就是行为的一部分：
	//   1) access     —— 名单命中即整条丢弃，必须最先。排到审查之后就会先建会话、
	//                    先留上下文，被丢弃的人反而留下了痕迹。
	//   2) moderation —— 入站审查 / 黑名单 / 防刷。拦截的事件不进入任何路由
	//                    （连"只记录"的兜底路由也不执行）——被拉黑的人不该留下上下文。
	//   3) admin      —— 管理命令。未知命令必须能落回普通路由（别把聊天里的斜杠吃掉）。
	//
	// 这三段的相对顺序由 inboundPreHookOrder + checkPreHookOrder 在启动时断言，
	// 不再只写在注释里（见该函数）。
	engine.UsePreNamed(preHookAccess, accessRule(accessCtl, superUsers, catalog, auditLog, lg))

	modEngine, modErr := buildModeration(cfg, accessCtl.Roles, lg)
	if modErr != nil {
		lifecycle.Error("cannot build inbound moderation", "error", modErr)
		return 1
	}
	if modEngine != nil {
		engine.UsePreNamed(preHookModeration, moderationPreHook(modEngine, catalog, auditLog, lg,
			func(ev *event.Event) agent.Role { return agentRoleFor(ev, accessCtl, superUsers) }))
	}
	// F-71：管理命令（/help、/ban、/unban、/banlist）。
	// 注意：/switch 已由既有路由处理，这里不重复注册——两条授权路径比没有更难维护。
	// 只在配置了超管时才启用：没有授权者就没有"管理"可言。
	if adminMod := buildAdminModule(cfg, accessCtl.Roles, auditLog, lg, modEngine, costTracker, personaMgr, promptHash); len(superUsers) > 0 {
		engine.UsePreNamed(preHookAdmin, func(c *router.Ctx) bool {
			if c == nil || c.Event == nil {
				return true
			}
			text := strings.TrimSpace(c.MessageString())
			if !strings.HasPrefix(text, "/") {
				return true
			}
			reply, derr := adminMod.Dispatch(c, admin.Request{
				Text: text, UserID: c.Event.UserID, GroupID: c.Event.GroupID,
				Source: admin.SourceMessage,
			})
			if errors.Is(derr, admin.ErrUnknownCommand) {
				// 不是管理命令：交回路由，别把普通聊天里的斜杠吃掉。
				return true
			}
			if derr != nil {
				lg.Component("admin").Warn("admin command failed", "error", derr, "command", text)
				return false
			}
			if reply == "" {
				// 未授权且配置为静默：不泄露命令是否存在，但审计已经记了。
				return false
			}
			target := outbound.PrivateTarget(c.Event.UserID)
			if c.Event.GroupID != 0 {
				target = outbound.GroupTarget(c.Event.GroupID)
			}
			if _, serr := sender.SendMany(context.Background(), target, []string{reply}, 0); serr != nil {
				lg.Component("admin").Warn("send admin reply failed", "error", serr)
			}
			return false
		})
	}

	// F-24：敏感词表热加载——改词表不必重启（编译失败保留旧表）。
	if w := watchSensitiveWords(listenCtx, cfg.Moderation.SensitiveWordsFile, cfg.Moderation.MaskReplacement, modEngine, lg); w != nil {
		defer w.Stop()
	}

	// F-24：人格目录热加载。改人格设定不必重启；替换只影响半静态段（F-65）。
	if w := watchPersonas(listenCtx, cfg.Prompt.EffectivePersonasDir(), personaReg, lg); w != nil {
		defer w.Stop()
	}

	// F-24：权限表热加载（仅当外部文件存在时监听；不存在则用内置基线）。
	if w := watchPolicyFile(listenCtx, cfg.Policy.File, policyTables, lg); w != nil {
		defer w.Stop()
	}

	// F-18：令牌桶限速（默认关闭）。超限事件会被整条丢弃——这是刻意的：
	// 限速的目的就是让刷屏不产生任何 LLM 调用，代价远低于额度被打爆。
	// F-24：规则**始终注册**，内部每次解引用当前参数，因此热加载（含"先关后开"）
	// 只需一次原子写，不必也无法重建已注册的 mid 钩子。
	userLimitHook := rateLimitedHook(catalog, auditLog, "user")
	groupLimitHook := rateLimitedHook(catalog, auditLog, "group")
	rateState := newRateLimitState(cfg.RateLimit, userLimitHook, groupLimitHook)
	userRule, groupRule := rateState.Rules()
	engine.UseMid(userRule)
	engine.UseMid(groupRule)
	// 限速规则**始终注册**（热加载要能"先关后开"），因此这里的"能力"指它是否在拦截。
	rateLimitOn := cfg.RateLimit.EffectiveEnabled()
	if rateLimitOn {
		lg.Component("ratelimit").Info("token bucket rate limiting is enabled",
			"user_per_minute", cfg.RateLimit.EffectiveUserPerMinute(), "user_burst", cfg.RateLimit.EffectiveUserBurst(),
			"group_per_minute", cfg.RateLimit.EffectiveGroupPerMinute(), "group_burst", cfg.RateLimit.EffectiveGroupBurst())
	}

	// F-48 延伸：空闲反思。它会**主动花钱**，因此默认关闭，且受四道闸门约束
	// （静默 / 有新内容 / 节流 / 每日预算），任一不满足就完全不调模型。
	if rs := buildReflection(cfg, hist, mem, sessions, lg); rs != nil {
		app.Go("memory-reflect", func(ctx context.Context) { rs.Run(ctx) })
	}

	// F-24：限速参数热加载。与前面的资产不同，它监听的是**配置文件本身**，
	// 因此重载要重建整个 Config 并校验；失败保留旧参数。
	if w := watchRateLimit(listenCtx, configPath, rateState, userLimitHook, groupLimitHook, lg); w != nil {
		defer w.Stop()
	}

	// F-17：单飞（反并发）。同一用户连点两次时，第二次在入口被拒。
	// 占位在 post 释放——引擎在 Handler panic 后仍会执行 post，因此不会永久卡住。
	singleflightOn := cfg.Singleflight.EffectiveEnabled()
	if singleflightOn {
		keyFn := func(c *router.Ctx) string { return fmt.Sprintf("%d:%d", c.Event.GroupID, c.Event.UserID) }
		if cfg.Singleflight.EffectiveKey() == "user" {
			keyFn = func(c *router.Ctx) string { return fmt.Sprintf("%d", c.Event.UserID) }
		}
		sf := router.NewSingleflight(keyFn)
		if cfg.Singleflight.EffectiveNotice() {
			sf.OnReject(func(c *router.Ctx) {
				target := outbound.PrivateTarget(c.Event.UserID)
				if c.Event.GroupID != 0 {
					target = outbound.GroupTarget(c.Event.GroupID)
				}
				if _, err := sender.SendMany(context.Background(), target, []string{"上一条还在处理中，稍等一下～"}, 0); err != nil {
					lg.Component("singleflight").Warn("cannot send busy notice", "error", err)
				}
			})
		}
		// 两半一起注册，见 runtime.go 的 useSingleflight。
		useSingleflight(engine, sf)
		lg.Component("singleflight").Info("single-flight middleware is enabled", "key", cfg.Singleflight.EffectiveKey())
	}

	// F-19：功能开关。未启用时等价于没有开关（默认全开）。
	toggles := newToggles(cfg, lg)
	if cfg.Toggle.EffectiveEnabled() {
		routes.OnMessage(router.Prefix("/switch")).
			Named("switch").
			Priority(router.PriorityEarly).
			Block(true).
			Handle(func(c *router.Ctx) {
				switchCommand(c, toggles, sender, roleForEvent(c.Event, accessCtl, superUsers))
			})
	}

	routes.OnMessage(replyRule(cfg)).
		Named("reply").
		UsePre(toggles.Rule("reply")).
		Priority(router.PriorityEarly).
		Block(true).
		Handle(func(c *router.Ctx) { enqueue(c, true) })

	routes.OnMessage(router.Always()).
		Named("record").
		Priority(router.PriorityLate).
		Handle(func(c *router.Ctx) { enqueue(c, false) })

	// 路由顺序是**行为**：兜底路由（record）一旦排到 reply 前面就会吃掉所有消息，
	// 机器人表现为完全不回复，而进程健康、探针正常、日志无异常。
	// 契约与检查见 route_contract.go，由 route_contract_test.go 钉住。
	if err := checkRouteTable(routes, inboundRouteContract); err != nil {
		lifecycle.Error("route table violates the assembly contract", "error", err)
		return 1
	}

	// 回复 worker 池：把一轮回复从读循环里挪出去，并保证 panic 不带走流水线。
	// 循环本体见 replyworkers.go 与 replyworkers_test.go。
	startReplyWorkers(app, replyWorkers, jobs, pipeline.Handle, &inflight, lg)

	// ---- 阶段：事件入口 ----
	// 有界背压队列 + 读循环回调；两条不能违反的约束（读循环里不调平台 API、
	// 临时路由优先）都收在 eventIngress 里，见 ingress.go 与 ingress_test.go。
	ingress := newEventIngress(ingressDeps{
		Engine:     engine,
		Sessions:   sessions,
		Catalog:    catalog,
		Log:        lg,
		ListenCtx:  listenCtx,
		AccessCtl:  accessCtl,
		SuperUsers: superUsers,
	})
	eventQueue = ingress.Queue()
	ingress.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = ingress.Close(ctx)
	}()

	// ws-session：连接 + 读循环，断线后按退避重连（见 wssession.go）。
	// 重连不是可选项：这个 goroutine 一旦退出就再也没人把连接建起来。
	app.Go("ws-session", func(ctx context.Context) {
		runWSSession(ctx, wsSessionDeps{
			Client:    ws,
			Sink:      ingress.Sink,
			ListenCtx: listenCtx,
			OnUp:      func() { wsUp.Store(true) },
			// 断开必须反映到探针上：否则 /readyz 会在机器人已经收不到消息时继续报 ready。
			OnDown: func() { wsUp.Store(false) },
			OnLogin: func(loginCtx context.Context) {
				go func() {
					resp, err := ws.Call(loginCtx, transport.Request{Action: "get_login_info"})
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
			},
			Backoff: retry.Default().Delay,
			Sleep:   sleepCtx,
			Log:     lg,
		})
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

	// 装配到此结束：断言入站 pre 钩子的执行顺序符合契约。
	// 顺序错了不会编译失败、也不会让任何测试变红——只会在生产里表现成
	// "被拉黑的人先被建了会话"或"命令被吞掉"，所以在这里 fail-fast。
	if err := checkPreHookOrder(engine.PreHookNames()); err != nil {
		lifecycle.Error("inbound pre-hook order violates the contract", "error", err)
		return 1
	}

	app.MarkRunning()
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	if provider == "" {
		provider = "openai"
	}
	// F-79：启动日志里的能力清单由**本次装配的事实**推出（见 capabilities.go），
	// 不是一份手写清单——手写清单会随接线悄悄过期。
	lifecycle.Info("agentbot started",
		"transport", cfg.Transport.Mode,
		"llm_provider", provider,
		"capabilities", assembleCapabilities(capabilityInputs{
			TransportMode:   cfg.Transport.Mode,
			AuthConfigured:  cfg.Transport.AccessToken != nil || cfg.Transport.SignatureSecret != nil,
			Cost:            costTracker != nil,
			Budget:          budget,
			Policy:          policyTables,
			Personas:        personaReg != nil,
			Memory:          mem != nil,
			History:         hist,
			Semcache:        semCache != nil,
			Streaming:       streamFactory != nil,
			Moderation:      modEngine != nil,
			Admin:           len(superUsers) > 0,
			Toggles:         cfg.Toggle.EffectiveEnabled(),
			ProactiveMemory: cfg.Agent.ProactiveMemory.EffectiveEnabled(),
			ToolHint:        cfg.Agent.Enabled && cfg.Agent.ToolHint.EffectiveEnabled(),
			PromptEngine:    promptEngine != nil,
			AuditLog:        auditLog != nil,
			OpsHTTP:         cfg.Ops.EffectiveEnabled(),
			RateLimit:       rateLimitOn,
			Singleflight:    singleflightOn,
		}),
	)

	// F-68/F-69：监听放在最后启动——此时探针要读的状态都已赋值，
	// 抓取不会与启动初始化并发。
	opsSrv, oerr := buildOps(cfg, catalog, readinessChecks(st, &wsUp, provider), lg)
	if oerr != nil {
		lifecycle.Error("cannot start ops listener", "error", oerr)
		return 1
	}
	defer closeOps(opsSrv, lg)
	if opsSrv != nil {
		opsSrv.SetReady(true)
	}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	// 关闭顺序（ops 先置未就绪 → 排空事件队列 → Shutdown 组件）见 shutdown.go，
	// 并由 shutdown_test.go 钉住。
	// Ops 必须走 opsReadiness：ops 关闭时它是 nil，直接塞进接口会得到
	// "非 nil 的接口持有 nil 指针"，判空失效后在关闭路径上 panic。
	return awaitShutdown(shutdownDeps{
		App:        app,
		Ops:        opsReadiness(opsSrv),
		EventQueue: eventQueue,
		Log:        lg,
		Stderr:     stderr,
		Timeout:    timeout,
		Signals:    sig,
		Exit:       os.Exit,
	})
}
