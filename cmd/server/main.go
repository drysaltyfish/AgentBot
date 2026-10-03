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
	"strconv"
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
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/retry"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
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
	showStats := fs.Bool("stats", false, "打印用量台账后退出（F-85）")
	exportMem := fs.String("export-memories", "", "把全部记忆导出到该 JSONL 文件后退出（F-88）")
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
	if *showStats {
		return runStats(cfg, stdout, stderr)
	}
	if *exportMem != "" {
		return runExportMemories(cfg, *exportMem, stdout, stderr)
	}
	return serve(cfg, stderr)
}

// runExportMemories 把全部记忆导出为 JSONL（F-88）。
//
// 导出**跨作用域**，因此它是维护命令而不是会话内能力——
// 会话内只能看见自己的作用域。
func runExportMemories(cfg *config.Config, path string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.Open(ctx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: durationOr(cfg.Store.BusyTimeout, store.DefaultBusyTimeout),
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = st.Close() }()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = f.Close() }()

	n, err := st.ExportMemories(ctx, f)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	scopes, err := st.MemoryScopes(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "已导出 %d 条记忆（%d 个作用域）到 %s\n", n, len(scopes), path)
	return 0
}

// runStats 打印用量台账（F-85）。
//
// 这条命令存在的意义就是让"缓存命中率与花费"可以**查**，而不是只能翻日志。
func runStats(cfg *config.Config, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(ctx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: durationOr(cfg.Store.BusyTimeout, store.DefaultBusyTimeout),
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = st.Close() }()

	tot, err := st.UsageTotals(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "数据库: %s\n", st.Path())
	_, _ = fmt.Fprintf(stdout, "消息总数: %d\n", tot.MessageCount)
	_, _ = fmt.Fprintf(stdout, "请求总数: %d（工具调用 %d 次）\n", tot.Requests, tot.ToolCalls)
	_, _ = fmt.Fprintf(stdout, "token: 输入 %d / 输出 %d / 推理 %d\n",
		tot.InputTokens, tot.OutputTokens, tot.ReasoningTokens)
	_, _ = fmt.Fprintf(stdout, "前缀缓存: 命中 %d / 未命中 %d -> %.1f%%\n",
		tot.CacheHitTokens, tot.CacheMissTokens, tot.CacheHitRatio()*100)
	_, _ = fmt.Fprintf(stdout, "估计成本: $%.4f（价格版本 %q）\n", tot.EstimatedCostUSD, tot.PricingVersion)

	top, err := st.TopSessions(ctx, 5)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if len(top) > 0 {
		_, _ = fmt.Fprintln(stdout, "\n花费最高的会话:")
		for _, u := range top {
			_, _ = fmt.Fprintf(stdout, "  %-28s 请求 %4d  token 输入 %8d  命中 %5.1f%%  成本 $%.4f\n",
				u.SessionKey, u.Requests, u.InputTokens, u.CacheHitRatio()*100, u.EstimatedCostUSD)
		}
	}
	return 0
}

// quotedResolver 解析引用消息的内容（QQ 的"回复"功能）。
//
// 为什么需要它：OneBot 的 reply 段只给一个 message_id，**被引用的内容不在事件里**。
// 不解析的话，模型只看到 "[回复]"，根本不知道对方在回哪句话——
// 表现为"机器人看不懂我在回复它过去的话"。
//
// 缓存：同一条消息常被反复引用，缓存能省掉绝大部分 get_msg；
// TTL：消息可以被撤回，缓存过期后重新解析，避免长期显示已撤回的内容。
type quotedResolver struct {
	mu    sync.Mutex
	cache map[string]quotedEntry
}

type quotedEntry struct {
	text string
	at   time.Time
}

// quotedCacheTTL 是引用解析结果的缓存时长。
const quotedCacheTTL = 10 * time.Minute

func newQuotedResolver() *quotedResolver {
	return &quotedResolver{cache: map[string]quotedEntry{}}
}

// resolve 把消息里所有引用段的内容填好，返回成功填充的段数。
//
// 失败**不阻断**消息处理：解析不到就保留 "[回复]" 占位符并告警，
// 宁可信息少一点，也不能因为一次 API 调用失败就丢掉整条消息。
func (r *quotedResolver) resolve(ctx context.Context, caller transport.Caller, msg event.Message) int {
	ids := msg.ReplyIDs()
	if len(ids) == 0 {
		return 0
	}
	filled := 0
	for _, id := range ids {
		text, ok := r.lookup(id)
		if !ok {
			var err error
			text, err = transport.GetMsgText(ctx, caller, id)
			if err != nil {
				// 调用方负责记日志；这里只保证不阻断。
				continue
			}
			if text == "" {
				continue
			}
			r.store(id, text)
		}
		filled += msg.SetReplyText(id, text)
	}
	return filled
}

func (r *quotedResolver) lookup(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[id]
	if !ok || time.Since(e.at) > quotedCacheTTL {
		return "", false
	}
	return e.text, true
}

func (r *quotedResolver) store(id, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 有界：引用解析是热路径，缓存不能无界增长。
	const maxEntries = 512
	if len(r.cache) >= maxEntries {
		for k, e := range r.cache {
			if time.Since(e.at) > quotedCacheTTL {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxEntries {
			for k := range r.cache {
				delete(r.cache, k)
				break
			}
		}
	}
	r.cache[id] = quotedEntry{text: text, at: time.Now()}
}

// pendingStoreAdapter 把持久层适配成会话层的在途记录接口（F-86）。
//
// 中间隔一层是因为会话层不该知道 SQL 长什么样：它只需要"存一条等待 / 结束一条等待"。
type pendingStoreAdapter struct{ st *store.Store }

func (a pendingStoreAdapter) SavePending(ctx context.Context, r session.PendingRecord) error {
	return a.st.UpsertPending(ctx, store.Pending{
		ID: r.ID, SessionKey: r.SessionKey, Kind: r.Kind, Payload: r.Payload,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Status: store.PendingStatusPending,
	})
}

func (a pendingStoreAdapter) FinishPending(ctx context.Context, id, status, note string) error {
	return a.st.CompletePending(ctx, id, status, note)
}

// pendingSessionTarget 由会话键还原出投递目标。
//
// 群消息投群、私聊投人——**不猜测**：键里还原不出的信息不编造。
func pendingSessionTarget(key string) (outbound.Target, bool) {
	parts := strings.Split(key, ":")
	if len(parts) < 3 {
		return outbound.Target{}, false
	}
	groupID, err1 := strconv.ParseInt(parts[1], 10, 64)
	userID, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return outbound.Target{}, false
	}
	if groupID != 0 {
		return outbound.GroupTarget(groupID), true
	}
	if userID != 0 {
		return outbound.PrivateTarget(userID), true
	}
	return outbound.Target{}, false
}

// recoverPending 处理重启时残留的在途记录（F-86）。
//
// **诚实的说明**：我们只能通知，不能真正续跑。等待下一条消息与等待人工审批都挂在
// 一次正在执行的调用上（阻塞在 channel 上），进程重启后那次调用已经不存在，
// 没有东西可以"恢复"。因此这里做两件事：
//  1. 未过期的：告诉对方"刚才重启了，那个等待被打断"，避免一直干等；
//  2. 已过期的：告诉对方"等太久了，已作废"。
//
// 状态分别置为 orphaned / expired，**保留记录**而不是删除——
// "谁在什么时候批准/超时"正是审计要回答的。
//
// 之所以不做成"真正续跑"：那需要把等待变成可重放的持久工作流（谁在等、等到什么、
// 等到之后干什么），那是一个独立得多的特性，不该塞进这一条里假装完成。
func recoverPending(ctx context.Context, st *store.Store, sender *outbound.Sender, lg *observe.Logger) {
	plog := lg.Component("pending")

	rows, err := st.ListPending(ctx, "")
	if err != nil {
		plog.Warn("cannot list pending operations; recovery skipped", "error", err)
		return
	}
	expired, err := st.ExpirePending(ctx, 0)
	if err != nil {
		plog.Warn("cannot expire stale pending operations", "error", err)
	}

	notify := func(p store.Pending, expiredAlready bool, note string) {
		target, ok := pendingSessionTarget(p.SessionKey)
		if !ok {
			// 还原不出目标就不猜：标成孤儿并告警。
			if err := st.CompletePending(ctx, p.ID, store.PendingStatusOrphaned,
				"无法从会话键还原投递目标"); err != nil {
				plog.Warn("cannot mark pending as orphaned", "error", err, "id", p.ID)
			}
			plog.Warn("pending operation has an unparsable session key", "id", p.ID, "session_key", p.SessionKey)
			return
		}
		if _, serr := sender.SendMany(ctx, target, []string{note}, 0); serr != nil {
			plog.Warn("cannot notify the session about an interrupted wait", "error", serr, "id", p.ID)
			return
		}
		status := store.PendingStatusOrphaned
		if expiredAlready {
			status = store.PendingStatusExpired
		}
		if err := st.CompletePending(ctx, p.ID, status, "重启后已通知原会话"); err != nil {
			plog.Warn("cannot close pending operation", "error", err, "id", p.ID)
		}
	}

	for _, p := range expired {
		notify(p, true, "刚才那件事等太久了，已经作废啦；要办的话请再说一次～")
	}

	expiredIDs := make(map[string]struct{}, len(expired))
	for _, p := range expired {
		expiredIDs[p.ID] = struct{}{}
	}
	notified := 0
	for _, p := range rows {
		if _, done := expiredIDs[p.ID]; done {
			continue
		}
		notified++
		notify(p, false, "刚才我重启了一下，之前正在等的那件事被打断了；麻烦你再发起一次～")
	}
	plog.Info("pending operations recovered",
		"total", len(rows), "expired", len(expired), "notified", notified)

	// 有界性：清掉早已结束的记录。
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if pruned, perr := st.PrunePending(ctx, cutoff); perr != nil {
		plog.Warn("cannot prune pending history", "error", perr)
	} else if pruned > 0 {
		plog.Info("pruned finished pending records", "count", pruned)
	}
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

// buildJudgeLLM 构造**关闭思考**的语义判官客户端（F-87）。
//
// 关闭思考是刻意的：判定只需要一个标签，开着思考会为它多花几百个 token 与几秒延迟。
// 复用同一个 provider 与端点，只是换一组模型参数——因此不需要第二份配置。
func buildJudgeLLM(cfg *config.Config, lg *observe.Logger) (llm.LLM, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	switch provider {
	case "echo":
		// 假模型判不了语义；返回 nil 让调用方退回确定性判据。
		lg.Component("memory").Info("echo provider cannot judge semantics; using the deterministic threshold")
		return nil, nil
	case "", "openai", "deepseek":
		client := httpx.NewClient(httpx.Config{
			Timeout:      llmTimeout(cfg),
			MaxBytes:     httpx.Defaults().MaxBytes,
			MaxRedirects: 3,
		})
		noThinking := false // 判官不需要思考
		base := llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL:  baseURL(cfg, provider),
			APIKey:   stringOr(cfg.LLM.APIKey, ""),
			Model:    cfg.LLM.Model,
			Client:   client,
			Thinking: &noThinking,
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

// promptSnapshotKeep 是每个会话保留的提示词快照条数（F-89 的环形保留）。
//
// 200 轮足够回溯"前缀是从哪一轮开始不稳的"，而快照本身只存指纹，体积很小。
const promptSnapshotKeep = 200

func floatOr(p *float64, fallback float64) float64 {
	if p != nil {
		return *p
	}
	return fallback
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

// speakerName 返回群聊里用来标识发言人的名字。
//
// 群名片优先于昵称：群里大家认的是群名片。
// 私聊返回空串——只有两个人，每句都加前缀是纯噪声。
func speakerName(sender event.Sender, groupID int64) string {
	if groupID == 0 {
		return ""
	}
	if name := strings.TrimSpace(sender.Card); name != "" {
		return name
	}
	return strings.TrimSpace(sender.Nickname)
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
func buildAgent(cfg *config.Config, model llm.LLM, sysPrompt string, hist history.History, st *store.Store, lg *observe.Logger) (agent.Agent, agent.Memory, error) {
	if !cfg.Agent.Enabled {
		return &agent.DirectAgent{LLM: model, SystemPrompt: sysPrompt}, nil, nil
	}

	registry := tool.New(tool.WithWarnFunc(func(msg string) {
		lg.Component("tool").Warn(msg)
	}))

	// F-87：记忆落在持久层。
	//
	// 语义判官用**关闭思考**的模型：只在相似度落在歧义带时才问一次，
	// 因此绝大多数字记忆写入不付额外调用。判官不可用时退回确定性判据，
	// 写入照常成功——判官只是把判定做得更准，不是必须依赖。
	maxPerScope := intOr(cfg.Agent.MemoryMax, 64)
	var judge memory.Judge
	if boolOr(cfg.Agent.MemoryJudge.Enabled, true) {
		jm, jerr := buildJudgeLLM(cfg, lg)
		switch {
		case jerr != nil:
			lg.Component("lifecycle").Warn("cannot build the memory judge; ambiguous writes use the deterministic threshold", "error", jerr)
		case jm == nil:
			// echo provider：判不了语义，保持 judge 为 nil。
		default:
			judge = memory.NewLLMJudge(jm, func(msg string) { lg.Component("memory").Debug(msg) })
			lg.Component("memory").Info("semantic memory judge is enabled (thinking off)")
		}
	}

	var (
		mem     agent.Memory
		memImpl *memory.Store
		// 注意：不能把 nil 的 *memory.Store 直接塞进接口——那样接口不为 nil，
		// 工具会以为管理能力可用，调用时才炸。
		memAdmin builtin.MemoryAdmin
	)
	if boolOr(cfg.Agent.Memory, true) {
		memImpl = memory.New(memory.Options{
			Store: st, Judge: judge, MaxPerScope: maxPerScope,
			Warn: func(msg string) { lg.Component("memory").Info(msg) },
		})
		mem = memImpl
		memAdmin = memImpl
		lg.Component("memory").Info("long-term memory is stored in the database",
			"max_per_scope", maxPerScope, "judge", judge != nil)
	}

	// 一次性迁移：旧记忆文件导入（幂等）。
	if legacy := strings.TrimSpace(cfg.Agent.MemoryFile); legacy != "" && mem != nil {
		migCtx, cancelMemMig := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelMemMig()
		imported, skipped, ierr := st.ImportMemoriesJSONL(migCtx, legacy)
		switch {
		case errors.Is(ierr, store.ErrImportSourceMissing):
			lg.Component("memory").Info("no legacy memory file to import", "path", legacy)
		case ierr != nil:
			lg.Component("lifecycle").Warn("legacy memory import failed", "error", ierr, "path", legacy)
		default:
			lg.Component("memory").Info("legacy JSONL memory imported",
				"path", legacy, "imported", imported, "skipped", skipped)
		}
	}

	// 内置工具：先全量注册再按配置裁剪，这样顺序始终等于内置顺序（前缀缓存需要稳定）。
	deps := builtin.Deps{Memory: mem, HTTP: httpx.Defaults(), Now: time.Now, History: hist, MemoryAdmin: memAdmin}
	if err := builtin.Register(registry, deps); err != nil {
		return nil, nil, fmt.Errorf("register builtin tools: %w", err)
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
			return nil, nil, fmt.Errorf("register virtual actions: %w", err)
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
	return react, mem, nil
}

// replyPipeline 收拢回复链路的依赖。
//
// 收成一个结构体是因为参数已经涨到九个——继续加下去，调用点会变成一长串位置参数，
// 既容易传错顺序，也让"这条链路到底依赖什么"看不清楚。
type replyPipeline struct {
	brain    agent.Agent
	sender   *outbound.Sender
	sessions *session.Manager
	asm      *conversation.Assembler
	memory   agent.Memory
	autoMem  *agent.MemoryCommand
	store    *store.Store
	price    llm.Price
	quoted   *quotedResolver
	timeout  time.Duration
	shape    sendShape
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
	// speaker 是发言人在群里的标识（私聊为空）。
	// 群里不加这个，模型就分不清 A 说的和 B 说的——记忆也会归错人。
	speaker string
	// message 是原始消息：引用解析要在 worker 里做，sink 里调 API 会死锁。
	message event.Message
	// caller 用于调用平台 API（get_msg）。
	caller transport.Caller
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
	// F-83：持久层。打不开就启动失败——不得静默降级为内存（那会悄悄丢数据）。
	// 打开与迁移给一个独立预算：卡住时要在启动阶段暴露，而不是拖到第一条消息。
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.Open(openCtx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: durationOr(cfg.Store.BusyTimeout, store.DefaultBusyTimeout),
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

	histItems := historyTurns(cfg) * 2
	// **存储**保留量远大于呈现窗口：否则 recall_history 只能返回已经出现在
	// 提示词里的内容，等于摆设。两者分开是让那个工具真正有用的前提。
	retention := intOr(cfg.History.Retention, 400)
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
	if boolOr(cfg.Agent.ProactiveMemory.Enabled, true) {
		sysPrompt = agent.ComposeSystemPrompt(sysPrompt,
			agent.ProactiveMemoryInstruction(cfg.Agent.ProactiveMemory.Instruction))
	}
	// 工具使用提示：被引用内容只有一句，很久远时缺上下文——
	// 告诉模型它可以用 recall_history 回溯，否则它不会想到这个手段。
	if cfg.Agent.Enabled && boolOr(cfg.Agent.ToolHint.Enabled, true) {
		sysPrompt = agent.ComposeSystemPrompt(sysPrompt,
			agent.ToolUsageInstruction(cfg.Agent.ToolHint.Instruction))
	}
	asm := conversation.New(conversation.Options{System: sysPrompt, MaxHistory: histItems})
	lg.Component("llm").Info("cache-first layout pinned",
		"prefix_hash", asm.PrefixHash(), "prefix_runes", len([]rune(sysPrompt)),
		"history_items", histItems, "trim_high_water", histItems, "trim_low_water", histItems*3/4)

	// Agent：启用时走 ReAct（带工具），否则是直连 LLM。调用方对两条路径同形。
	brain, mem, err := buildAgent(cfg, model, sysPrompt, hist, st, lg)
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
	// F-86：恢复残留的在途记录（通知原会话；已过期的作废）。
	{
		recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 30*time.Second)
		recoverPending(recoverCtx, st, sender, lg)
		cancelRecover()
	}

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
		"enabled", boolOr(cfg.Agent.ProactiveMemory.Enabled, true))

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

	// F-84：引用消息的内容要查一次平台 API；解析在 worker 里做，见 handleReply。
	quoted := newQuotedResolver()

	pipeline := replyPipeline{
		brain: brain, sender: sender, sessions: sessions, asm: asm,
		memory: mem, autoMem: autoMem, timeout: timeout, shape: shape,
		store: st, price: price, quoted: quoted,
	}

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
				speaker: speakerName(c.Event.Sender, c.Event.GroupID),
				message: c.Event.Message,
				caller:  c.Caller(),
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
						handleReply(ctx, lg, pipeline, j)
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
		// 引用解析因此放在回复 worker 里做（见 handleReply）。
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
func handleReply(ctx context.Context, lg *observe.Logger, p replyPipeline, j replyJob) {
	rlog := lg.Component("reply")
	callCtx, cancel := context.WithTimeout(observe.WithTraceID(ctx, j.traceID), p.timeout)
	defer cancel()

	// F-48 的规则触发：显式说"记住：xxx"时无条件写入，不取决于模型是否调用工具。
	// 写在跑模型之前，因此这一轮的提示词里就已经带上它。
	if p.autoMem != nil && p.memory != nil {
		if fact, ok := p.autoMem.Extract(j.text); ok {
			saveCtx := agent.WithMemoryScope(callCtx, j.key.String())
			if err := p.memory.Save(saveCtx, fact); err != nil {
				rlog.Warn("auto memory save failed", "error", err, "runes", len([]rune(fact)))
			} else {
				rlog.Info("memory auto-saved from an explicit command", "runes", len([]rune(fact)))
			}
			// 标记本轮已捕获：模型随后若再调 save_memory，会被告知无需重复保存。
			callCtx = agent.WithMemoryCaptured(callCtx)
		}
	}

	// F-84 的引用解析放在这里：worker 是独立 goroutine，不会卡住传输层的读循环。
	if ids := j.message.ReplyIDs(); len(ids) > 0 {
		if p.quoted != nil && j.caller != nil {
			qctx, cancelQuote := context.WithTimeout(callCtx, 8*time.Second)
			filled := p.quoted.resolve(qctx, j.caller, j.message)
			cancelQuote()
			if filled < len(ids) {
				rlog.Warn("could not resolve every quoted message",
					"quoted", len(ids), "resolved", filled, "summary", j.message.Summary())
			} else {
				rlog.Info("quoted messages resolved", "count", filled)
			}
			// 解析后重算：被引用的内容现在进入了这一轮的输入。
			if resolved := strings.TrimSpace(j.message.Summary()); resolved != "" {
				j.text = resolved
			}
		}
	}

	queryText := j.text
	if j.speaker != "" {
		queryText = j.speaker + "：" + j.text
	}

	//nolint:contextcheck // 会话回收时的在途收尾走后台 ctx，与本次请求的生命周期无关
	sess := p.sessions.GetOrCreate(j.key)
	histKey := j.key.String()
	items, err := sess.Hist.Messages(callCtx, histKey)
	if err != nil {
		// 读不到历史不该拒绝服务：退化成单轮，但要留下痕迹。
		rlog.Warn("cannot read history; falling back to a single turn", "error", err)
	}

	// 两条路径（ReAct / 直连）在调用方看完全同形。
	// 走 ReAct 时，记忆的注入位置由 Agent 按 ADR-0002 处理（system 之后、历史之前）。
	out, runErr := p.brain.Run(callCtx, agent.Input{
		Query:      queryText,
		History:    conversation.ToMessages(items),
		SessionKey: j.key,
		Role:       j.role,
	})

	// F-85：把这一轮的用量累加进台账。失败只告警——用量统计与用户请求的价值不对等，
	// 不能让它拖垮回复。
	if p.store != nil {
		cost := 0.0
		if p.price.Enabled() {
			cost = p.price.Cost(out.Usage)
		}
		uerr := p.store.AddUsage(callCtx, j.key.String(), store.UsageDelta{
			Requests:        int64(out.LLMCalls),
			ToolCalls:       int64(len(out.ToolCalls)),
			InputTokens:     int64(out.Usage.PromptTokens),
			OutputTokens:    int64(out.Usage.CompletionTokens),
			CacheHitTokens:  int64(out.Usage.PromptCacheHitTokens),
			CacheMissTokens: int64(out.Usage.PromptCacheMissTokens),
			ReasoningTokens: int64(out.Usage.ReasoningTokens),
			CostUSD:         cost,
			PricingVersion:  p.price.Version,
		})
		if uerr != nil {
			rlog.Warn("cannot record usage; metrics will be incomplete", "error", uerr)
		}
	}

	// F-89：记录本轮实际发送的消息指纹，并判断前缀是否**意外**变化。
	// 这是把"前缀为什么变了"从事后猜变成当场知道的那一步。
	if p.store != nil && len(out.PromptDigest) > 0 {
		snap, serr := p.store.RecordPromptSnapshot(callCtx, j.key.String(), out.PromptDigest, out.MemoryDigest)
		switch {
		case serr != nil:
			rlog.Warn("cannot record prompt snapshot", "error", serr)
		default:
			rlog.Info("prompt snapshot", "relation", snap.Relation,
				"messages", snap.MessageCount, "common_prefix", snap.CommonPrefix, "slid_by", snap.SlidBy)
			switch snap.Relation {
			case store.RelationMemoryChanged:
				// 记忆块变了：这是 ADR-0002 接受的代价，只需要知道"代价发生在这里"，
				// 不该当成异常告警——否则告警会一直响，等于没有告警。
				rlog.Info("prompt prefix changed because the memory block changed",
					"common_prefix", snap.CommonPrefix, "messages", snap.MessageCount)
			case llm.RelationDiverged:
				// 既不是追加、不是窗口滑动、也不是记忆变更：前缀被改写了，这才值得报。
				rlog.Warn("prompt prefix diverged unexpectedly; prefix cache hits will drop",
					"common_prefix", snap.CommonPrefix, "messages", snap.MessageCount)
			}
		}
	}

	// F-40：模型主动结束本轮。这**不是失败**，但也不发任何消息。
	if errors.Is(runErr, agent.ErrEndOfTurn) {
		if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindUser, Content: queryText}); err != nil {
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
	if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindUser, Content: queryText}); err != nil {
		rlog.Warn("cannot append user turn", "error", err)
	}
	if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindAssistant, Content: reply}); err != nil {
		rlog.Warn("cannot append assistant turn", "error", err)
	}

	rlog.Info("llm call",
		"prefix_hash", p.asm.PrefixHash(),
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
	if p.shape.splitOnBlank {
		parts = outbound.SplitParagraphs(reply, p.shape.maxSegments)
	}
	if len(parts) == 0 {
		rlog.Warn("reply became empty after splitting")
		return
	}
	sent, err := p.sender.SendMany(callCtx, target, parts, p.shape.delay)
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
