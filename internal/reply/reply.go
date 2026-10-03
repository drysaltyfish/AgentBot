// Package reply 拥有"一次回复轮次"的完整行为。
//
// 它收拢的东西：显式记忆指令的自动写入、引用消息解析、用户轮次的落库、
// 调用模型、用量台账累加、提示词快照判定，以及按形态分段发送。
//
// 为什么单独成包：这些行为的**顺序**本身就是不变量（先记录再决定是否回复、
// 先解析引用再渲染输入、助手轮次只追加不改写），而它们原先藏在组合根的一个
// 两百行函数里，没有接口、无法测试，且每次改动都落在同一个文件上发酵。
package reply

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// Job 是一次待处理的来信。
type Job struct {
	Key         session.Key
	GroupID     int64
	UserID      int64
	Text        string
	TraceID     string
	Role        agent.Role
	SpeakerID   int64
	SpeakerName string
	// ShouldReply 为 false 时只记录不回复：群里的环境消息也是上下文。
	ShouldReply bool
	// Message 是原始消息：引用解析要在 worker 里做，sink 里调 API 会死锁。
	Message event.Message
	// Caller 用于调用平台 API（get_msg）。
	Caller transport.Caller
}

// Shape 描述回复的发送形态（是否按空行拆分、连发间隔、最多几条）。
type Shape struct {
	SplitOnBlank bool
	Delay        time.Duration
	MaxSegments  int
}

// Deps 收拢回复链路的依赖。
//
// 收成一个结构体是因为参数已经涨到九个——继续加下去，调用点会变成一长串位置参数，
// 既容易传错顺序，也让"这条链路到底依赖什么"看不清楚。
type Deps struct {
	Brain     agent.Agent
	Sender    *outbound.Sender
	Sessions  *session.Manager
	Assembler *conversation.Assembler
	Memory    agent.Memory
	AutoMem   *agent.MemoryCommand
	Store     *store.Store
	Price     llm.Price
	Log       *observe.Logger
	Timeout   time.Duration
	Shape     Shape
	// Audit 接收本轮的审计记录（F-60）；为 nil 时不记录。
	Audit *audit.Logger
	// Catalog 接收本轮的指标（F-68）；为 nil 时不记录。
	Catalog *metrics.Catalog
}

// Pipeline 执行一次回复轮次。
type Pipeline struct {
	deps   Deps
	quoted *quotedResolver
}

// New 构造 Pipeline。引用解析的缓存是内部细节，不暴露给调用方。
func New(d Deps) *Pipeline {
	return &Pipeline{deps: d, quoted: newQuotedResolver()}
}

// Handle 执行一次回复轮次。
//
// 这里落实"缓存优先"：消息序列固定为 [不可变前缀] + [记忆] + [只追加历史] + [当前输入]
// （装配由 internal/conversation 拥有，见 ADR-0002），并记录缓存命中计量，
// 让命中率可观测、可回归。
func (p *Pipeline) Handle(ctx context.Context, j Job) {
	rlog := p.deps.Log.Component("reply")
	callCtx, cancel := context.WithTimeout(observe.WithTraceID(ctx, j.TraceID), p.deps.Timeout)
	defer cancel()

	// F-48 的规则触发：显式说"记住：xxx"时无条件写入，不取决于模型是否调用工具。
	// 写在跑模型之前，因此这一轮的提示词里就已经带上它。
	if p.deps.AutoMem != nil && p.deps.Memory != nil {
		if fact, ok := p.deps.AutoMem.Extract(j.Text); ok {
			saveCtx := agent.WithMemoryScope(callCtx, j.Key.String())
			if err := p.deps.Memory.Save(saveCtx, fact); err != nil {
				rlog.Warn("auto memory save failed", "error", err, "runes", len([]rune(fact)))
			} else {
				rlog.Info("memory auto-saved from an explicit command", "runes", len([]rune(fact)))
			}
			// 标记本轮已捕获：模型随后若再调 save_memory，会被告知无需重复保存。
			callCtx = agent.WithMemoryCaptured(callCtx)
		}
	}

	// F-84 的引用解析放在这里：worker 是独立 goroutine，不会卡住传输层的读循环。
	if ids := j.Message.ReplyIDs(); len(ids) > 0 {
		if p.quoted != nil && j.Caller != nil {
			qctx, cancelQuote := context.WithTimeout(callCtx, 8*time.Second)
			filled := p.quoted.resolve(qctx, j.Caller, j.Message)
			cancelQuote()
			if filled < len(ids) {
				rlog.Warn("could not resolve every quoted message",
					"quoted", len(ids), "resolved", filled, "summary", j.Message.Summary())
			} else {
				rlog.Info("quoted messages resolved", "count", filled)
			}
			// 解析后重算：被引用的内容现在进入了这一轮的输入。
			if resolved := strings.TrimSpace(j.Message.Summary()); resolved != "" {
				j.Text = resolved
			}
		}
	}

	// 这一轮的输入：正文保持**干净**，发言人/时间作为结构化字段。
	// 渲染只发生在装配层，因此检索返回的正文不带标签。
	turn := history.Item{
		Kind:        history.KindUser,
		Content:     j.Text,
		SpeakerID:   j.SpeakerID,
		SpeakerName: j.SpeakerName,
		At:          time.Now(),
		// 没被 @ 的消息是**环境消息**：按 token 预算压缩，不跟对话争窗口。
		Ambient: !j.ShouldReply,
	}
	queryText := turn.RenderText()

	//nolint:contextcheck // 会话回收时的在途收尾走后台 ctx，与本次请求的生命周期无关
	sess := p.deps.Sessions.GetOrCreate(j.Key)
	histKey := j.Key.String()

	// **先记录**：无论是否回复，这条消息都是后续对话的上下文。
	// 群里绝大多数消息不会被 @，但它们构成了模型理解"刚才在聊什么"的全部依据。
	if err := sess.Hist.Append(callCtx, histKey, turn); err != nil {
		rlog.Warn("cannot append user turn", "error", err)
	}
	if !j.ShouldReply {
		rlog.Debug("message recorded without replying",
			"group_id", j.GroupID, "user_id", j.UserID)
		return
	}

	items, err := sess.Hist.Messages(callCtx, histKey)
	if err != nil {
		// 读不到历史不该拒绝服务：退化成单轮，但要留下痕迹。
		rlog.Warn("cannot read history; falling back to a single turn", "error", err)
	}
	// 当前这条已经在历史里；取出来单独作为 Query，避免同一句在提示词里出现两次。
	if n := len(items); n > 0 {
		items = items[:n-1]
	}

	// 装配统计：让"环境消息被压成什么样"在日志里可见（否则只能靠猜）。
	if n := len(items); n > 0 {
		var amb int
		for _, it := range items {
			if it.Ambient {
				amb++
			}
		}
		rlog.Debug("history assembled", "items", n, "ambient", amb, "convo", n-amb)
	}

	// F-66：把会话与用户归属放进 ctx——成本装饰器据此做会话/用户维度的计量与配额。
	// 归属与记忆作用域是同一性质（"这次调用属于谁"），因此都走 ctx 而不是 Input：
	// agent 与 llm 不需要为了记账多知道一个与它们无关的概念。
	callCtx = cost.WithAttribution(callCtx, j.Key.String(), strconv.FormatInt(j.UserID, 10))

	// 两条路径（ReAct / 直连）在调用方看完全同形。
	// 记忆的注入位置与呈现窗口由装配器按 ADR-0002 处理（system 之后、历史之前）。
	out, runErr := p.deps.Brain.Run(callCtx, agent.Input{
		Query:      queryText,
		History:    items,
		SessionKey: j.Key,
		Role:       j.Role,
	})

	// F-85：把这一轮的用量累加进台账。失败只告警——用量统计与用户请求的价值不对等，
	// 不能让它拖垮回复。
	if p.deps.Store != nil {
		cost := 0.0
		if p.deps.Price.Enabled() {
			cost = p.deps.Price.Cost(out.Usage)
		}
		uerr := p.deps.Store.AddUsage(callCtx, j.Key.String(), store.UsageDelta{
			Requests:        int64(out.LLMCalls),
			ToolCalls:       int64(len(out.ToolCalls)),
			InputTokens:     int64(out.Usage.PromptTokens),
			OutputTokens:    int64(out.Usage.CompletionTokens),
			CacheHitTokens:  int64(out.Usage.PromptCacheHitTokens),
			CacheMissTokens: int64(out.Usage.PromptCacheMissTokens),
			ReasoningTokens: int64(out.Usage.ReasoningTokens),
			CostUSD:         cost,
			PricingVersion:  p.deps.Price.Version,
		})
		if uerr != nil {
			rlog.Warn("cannot record usage; metrics will be incomplete", "error", uerr)
		}
	}

	// F-60：每轮模型调用与每次工具调用都留一条审计；F-68 同步记录工具指标。
	// 审计写入是异步有界的，这里不会阻塞回复链路。
	result := audit.ResultOK
	if runErr != nil {
		result = audit.ResultError
	}
	if p.deps.Audit != nil {
		p.deps.Audit.Log(audit.Event{
			Type: audit.EventLLMCall, TraceID: j.TraceID, SessionKey: j.Key.String(),
			UserID: j.UserID, GroupID: j.GroupID, Action: "chat", Result: result,
			Tokens: out.Usage.PromptTokens + out.Usage.CompletionTokens,
		})
	}
	for _, st := range out.Steps {
		if st.Type != agent.StepAction {
			continue
		}
		status := "ok"
		if st.Error != "" {
			status = "error"
		}
		if p.deps.Audit != nil {
			p.deps.Audit.Log(audit.Event{
				Type: audit.EventToolCall, TraceID: j.TraceID, SessionKey: j.Key.String(),
				UserID: j.UserID, GroupID: j.GroupID, Action: st.ToolName,
				Result: audit.Result(status), DurationMS: st.DurationMS,
				Params: map[string]string{"input": st.ToolInput},
			})
		}
		if p.deps.Catalog != nil {
			p.deps.Catalog.ToolCalls.With(metrics.Labels{"tool": st.ToolName, "status": status}).Inc()
			p.deps.Catalog.ToolDuration.With(metrics.Labels{"tool": st.ToolName}).Observe(float64(st.DurationMS) / 1000)
		}
	}

	// F-89：记录本轮实际发送的消息指纹，并判断前缀是否**意外**变化。
	// 这是把"前缀为什么变了"从事后猜变成当场知道的那一步。
	if p.deps.Store != nil && len(out.PromptDigest) > 0 {
		snap, serr := p.deps.Store.RecordPromptSnapshot(callCtx, j.Key.String(), out.PromptDigest, out.MemoryDigest)
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
		rlog.Info("turn ended by end_action", "group_id", j.GroupID, "user_id", j.UserID)
		return
	}
	if runErr != nil {
		// F-66：配额拒绝必须回复提示，不能静默失败。用户看不到原因时会把同一条
		// 消息再发一次，正好又撞一次限——沉默在这里会放大问题而不是掩盖问题。
		if qerr := quotaDenied(runErr); qerr != nil {
			rlog.Warn("model call denied by cost quota",
				"scope", qerr.Scope, "period", qerr.Period, "limit", qerr.Limit, "used", qerr.Used)
			target := outbound.PrivateTarget(j.UserID)
			if j.GroupID != 0 {
				target = outbound.GroupTarget(j.GroupID)
			}
			if _, serr := p.deps.Sender.SendMany(callCtx, target, []string{"本会话的模型额度已用完，请稍后再试或联系管理员。"}, 0); serr != nil {
				rlog.Warn("cannot send quota notice", "error", serr)
			}
			return
		}
		rlog.Error("agent run failed", "error", runErr, "steps", len(out.Steps))
		return
	}

	text := strings.TrimSpace(out.Text)
	if text == "" {
		rlog.Warn("agent returned empty text", "steps", len(out.Steps), "finish_reason", out.FinishReason)
		return
	}

	// 只追加、绝不改写：这是下一轮还能命中前缀缓存的前提。
	// 用户那一轮已在开头记录，这里只补助手回复。
	if err := sess.Hist.Append(callCtx, histKey, history.Item{Kind: history.KindAssistant, Content: text}); err != nil {
		rlog.Warn("cannot append assistant turn", "error", err)
	}

	// 前缀指纹用于证明"不可变前缀"跨轮未变。装配器是组合根注入的；
	// 未注入时（例如只测轮次顺序的单元测试）省略它，而不是让 nil 解引用把整条链路打崩。
	prefixHash := ""
	if p.deps.Assembler != nil {
		prefixHash = p.deps.Assembler.PrefixHash()
	}

	rlog.Info("llm call",
		"prefix_hash", prefixHash,
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

	target := outbound.PrivateTarget(j.UserID)
	if j.GroupID != 0 {
		target = outbound.GroupTarget(j.GroupID)
	}

	// 真人是一条一条发的：按空行拆成多条分别发送，而不是一整块砸过去。
	parts := []string{text}
	if p.deps.Shape.SplitOnBlank {
		parts = outbound.SplitParagraphs(text, p.deps.Shape.MaxSegments)
	}
	if len(parts) == 0 {
		rlog.Warn("reply became empty after splitting")
		return
	}
	sent, err := p.deps.Sender.SendMany(callCtx, target, parts, p.deps.Shape.Delay)
	if err != nil {
		rlog.Error("send failed", "error", err, "sent", sent, "segments", len(parts))
		return
	}
	if p.deps.Catalog != nil {
		p.deps.Catalog.ActionsSent.With(metrics.Labels{"action": "send_msg", "status": "ok"}).Add(float64(len(parts)))
	}

	// 每条讯息都带上它自己的缓存命中率：这是"缓存优先"是否生效的唯一客观指标。
	rlog.Info("replied", "group_id", j.GroupID, "user_id", j.UserID,
		"runes", len([]rune(text)), "segments", len(parts),
		"cache_hit_ratio", fmt.Sprintf("%.1f%%", out.Usage.CacheHitRatio()*100),
		"cache_hit_tokens", out.Usage.PromptCacheHitTokens,
		"cache_miss_tokens", out.Usage.PromptCacheMissTokens)
}

// quotaDenied 从错误链里取出配额拒绝，非配额错误返回 nil。
func quotaDenied(err error) *cost.QuotaError {
	var qerr *cost.QuotaError
	if errors.As(err, &qerr) {
		return qerr
	}
	return nil
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
