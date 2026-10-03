// Package moderation 实现入站内容审查（F-57）与黑名单/防刷（F-58）。
//
// 设计要点：
//   - 统一决策类型 Decision：allow / mask（替换后文本）/ block（拒绝并给原因），
//     路由层可直接读取 Kind、Text、Reason 决定放行、改写或拦截。
//   - 敏感词判定复用 internal/textguard 的 AC 自动机；词表热加载通过
//     textguard.Engine 的原子 Swap 完成，不阻塞 Review。
//   - 黑名单按用户/群/IP 分档，支持带过期时间的临时封禁；存储抽象为包内
//     BanStore 接口，提供 MemoryBanStore 与 FileBanStore 两种实现，重启不丢。
//   - 防刷同时做速率与连续重复消息检测，超限返回明确原因并可自动临时封禁。
//   - 审查/拦截/限流事件通过注入的 AuditFunc 与 Metrics 上报（分别对应 F-60/F-68）。
//
// 本包不持有任何包级可变状态；所有状态都在实例上，时间源可注入以便确定性测试。
package moderation

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/drysaltyfish/agentbot/internal/textguard"
)

// DecisionKind 是审查结论的种类。
type DecisionKind uint8

// 决策种类；顺序即零值语义：零值代表放行。
const (
	// DecisionAllow 表示放行，Text 为原文。
	DecisionAllow DecisionKind = iota
	// DecisionMask 表示放行，但出口必须使用脱敏后的 Text。
	DecisionMask
	// DecisionBlock 表示拒绝，Reason 说明原因。
	DecisionBlock
)

// String 返回稳定的字符串形式（审计与日志用）。
func (k DecisionKind) String() string {
	switch k {
	case DecisionAllow:
		return "allow"
	case DecisionMask:
		return "mask"
	case DecisionBlock:
		return "block"
	default:
		return "unknown"
	}
}

// Decision 是一次审查的结论，可直接被路由层使用。
//
// 字段语义：Text 是出口应当采用的文本（allow 为原文、mask 为脱敏文本、
// block 无意义）；Reason/Rule/Score 用于回复话术与审计。
type Decision struct {
	Kind   DecisionKind
	Text   string
	Reason string
	Rule   string
	Score  float64
}

// Allowed 报告是否放行（allow 与 mask 都放行）。
func (d Decision) Allowed() bool { return d.Kind != DecisionBlock }

// Blocked 报告是否拦截。
func (d Decision) Blocked() bool { return d.Kind == DecisionBlock }

// Message 是一次待审查的入站消息（目前只承载文本）。
type Message struct {
	Text string
}

// Meta 描述消息的环境信息，用于区分正式对话与背景消息、定位黑名单主体。
type Meta struct {
	// UserID 是发送者 ID；0 表示未知（此时跳过用户维度）。
	UserID int64
	// GroupID 是群 ID；0 表示私聊或未知（此时跳过群维度）。
	GroupID int64
	// SelfID 是机器人自身 ID（黑名单保护用，通常由 Blacklist 持有）。
	SelfID int64
	// IP 是来源 IP；仅在与 IP 黑名单相关时填写。
	IP string
	// Addressed 为 true 表示面向机器人的正式对话（被 @、私聊或命令触发）；
	// false 表示背景消息，可套用 AmbientPolicy 的不同策略。
	Addressed bool
	// Role 是调用者角色名，仅作审计留档；权限判定由 Authorizer 负责。
	Role string
}

// SensitivePolicy 决定敏感词命中后的动作。
type SensitivePolicy uint8

// 敏感词策略；零值为脱敏放行。
const (
	// SensitiveMask 命中即脱敏，仍放行。
	SensitiveMask SensitivePolicy = iota
	// SensitiveBlock 命中即拦截。
	SensitiveBlock
)

// String 返回稳定的字符串形式。
func (p SensitivePolicy) String() string {
	switch p {
	case SensitiveMask:
		return "mask"
	case SensitiveBlock:
		return "block"
	default:
		return "unknown"
	}
}

// AmbientPolicy 是背景消息（未被 @ 的正式对话）的差异化策略。
type AmbientPolicy struct {
	// SkipGuards 为 true 时背景消息不跑 InboundGuard 链。
	SkipGuards bool
	// SkipAntiSpam 为 true 时背景消息不计入防刷统计。
	SkipAntiSpam bool
	// Sensitive 是背景消息采用的敏感词策略。
	Sensitive SensitivePolicy
}

// AuditFunc 接收一条审查/拦截记录（可适配 F-60 的 audit.Logger）。
type AuditFunc func(Record)

// Record 是一条审查审计记录。
type Record struct {
	At      time.Time
	Event   string
	UserID  int64
	GroupID int64
	Rule    string
	Reason  string
	Score   float64
	Text    string
}

// Metrics 接收计数器自增（可适配 F-68 指标）。
type Metrics interface {
	Inc(name string)
}

// MetricsFunc 让普通函数满足 Metrics；nil 时调用无副作用。
type MetricsFunc func(string)

// Inc 实现 Metrics。
func (f MetricsFunc) Inc(name string) {
	if f != nil {
		f(name)
	}
}

// 审计事件类型（与 F-60 的 inbound_blocked / rate_limited 对齐）与指标名。
const (
	EventInboundBlocked = "inbound_blocked"
	EventRateLimited    = "rate_limited"
	EventBanned         = "banned"

	MetricInboundBlocked = "moderation.inbound_blocked"
	MetricRateLimited    = "moderation.rate_limited"
	MetricBanned         = "moderation.banned"
)

// Options 是 Engine 的构造参数；零值可用（全部放行）。
type Options struct {
	// Matcher 是敏感词引擎（F-56），nil 表示不做敏感词审查。
	Matcher *textguard.Engine
	// Sensitive 是敏感词策略，零值 SensitiveMask。
	Sensitive SensitivePolicy
	// Ambient 是背景消息策略，nil 表示沿用默认策略。
	Ambient *AmbientPolicy
	// Blacklist 是黑名单，nil 表示不检查。
	Blacklist *Blacklist
	// AntiSpam 是防刷器，nil 表示不限流。
	AntiSpam *AntiSpam
	// Guards 是审查链的 guard 列表。
	Guards []InboundGuard
	// Guard 控制 guard 链的超时与失败策略。
	Guard GuardOptions
	// Audit 接收拦截记录，可为 nil。
	Audit AuditFunc
	// Metrics 接收指标自增，可为 nil。
	Metrics Metrics
	// Warn 接收非致命告警，可为 nil。
	Warn func(string)
	// Now 提供时间源，nil 表示 time.Now。
	Now func() time.Time
}

// Engine 把敏感词、guard 链、黑名单与防刷串成一次入站审查。
// 零值不可用，必须经 New 构造；构造完成后可并发调用 Review。
type Engine struct {
	matcher   *textguard.Engine
	sensitive SensitivePolicy
	ambient   *AmbientPolicy
	bans      *Blacklist
	spam      *AntiSpam
	chain     *GuardChain
	audit     AuditFunc
	metrics   Metrics
	warn      func(string)
	now       func() time.Time
}

// New 构造 Engine。
func New(opts Options) *Engine {
	e := &Engine{
		matcher:   opts.Matcher,
		sensitive: opts.Sensitive,
		ambient:   opts.Ambient,
		bans:      opts.Blacklist,
		spam:      opts.AntiSpam,
		audit:     opts.Audit,
		metrics:   opts.Metrics,
		warn:      opts.Warn,
		now:       opts.Now,
	}
	if e.now == nil {
		e.now = time.Now
	}
	if len(opts.Guards) > 0 {
		e.chain = NewGuardChain(opts.Guard, opts.Guards...)
	}
	return e
}

// SwapMatcher 原子替换敏感词自动机，实现不阻塞 Review 的词表热加载（F-56）。
func (e *Engine) SwapMatcher(m *textguard.Matcher) {
	if e == nil || e.matcher == nil {
		return
	}
	e.matcher.Swap(m)
}

// Sanitize 返回经敏感词脱敏后的文本，不做拦截（出口侧亦可复用）。
func (e *Engine) Sanitize(text string) string {
	if e == nil || e.matcher == nil {
		return text
	}
	return e.matcher.Replace(text)
}

// Blacklist 返回注入的黑名单（可为 nil）。
func (e *Engine) Blacklist() *Blacklist { return e.bans }

// Review 执行一次完整的入站审查。
//
// 顺序：黑名单（白名单优先）→ 防刷 → 敏感词 → guard 链。任一环节拦截即返回
// DecisionBlock 并写入审计；敏感词策略为 mask 时返回 DecisionMask 及脱敏文本。
func (e *Engine) Review(ctx context.Context, msg Message, meta Meta) (Decision, error) {
	if e == nil {
		return Decision{Kind: DecisionAllow, Text: msg.Text}, nil
	}

	policy := e.sensitive
	amb := AmbientPolicy{}
	hasAmb := false
	if !meta.Addressed && e.ambient != nil {
		amb = *e.ambient
		hasAmb = true
		policy = amb.Sensitive
	}

	if e.bans != nil {
		if d, ok := e.blacklistDecision(msg, meta); ok {
			e.record(EventInboundBlocked, meta, d)
			e.count(MetricInboundBlocked)
			return d, nil
		}
	}

	if e.spam != nil && !(hasAmb && amb.SkipAntiSpam) {
		if d, ok := e.spamDecision(msg, meta); ok {
			e.record(EventRateLimited, meta, d)
			e.count(MetricRateLimited)
			return d, nil
		}
	}

	decision := Decision{Kind: DecisionAllow, Text: msg.Text}
	if e.matcher != nil {
		if reps := e.matcher.Replacements(msg.Text); len(reps) > 0 {
			if policy == SensitiveBlock {
				d := Decision{
					Kind:   DecisionBlock,
					Text:   msg.Text,
					Reason: fmt.Sprintf("敏感词命中 %d 处", len(reps)),
					Rule:   "textguard",
					Score:  1,
				}
				e.record(EventInboundBlocked, meta, d)
				e.count(MetricInboundBlocked)
				return d, nil
			}
			decision = Decision{
				Kind:   DecisionMask,
				Text:   e.matcher.Replace(msg.Text),
				Reason: fmt.Sprintf("敏感词命中 %d 处，已脱敏", len(reps)),
				Rule:   "textguard",
			}
		}
	}

	if e.chain != nil && !(hasAmb && amb.SkipGuards) {
		v, err := e.chain.Check(ctx, msg, meta)
		if err != nil {
			e.warnf("moderation: guard chain: %v", err)
		}
		if v != nil && !v.Allow {
			d := Decision{Kind: DecisionBlock, Text: msg.Text, Reason: v.Reason, Rule: v.Rule, Score: v.Score}
			e.record(EventInboundBlocked, meta, d)
			e.count(MetricInboundBlocked)
			return d, nil
		}
	}

	return decision, nil
}

// blacklistDecision 依次检查用户、群、IP 三个维度。
func (e *Engine) blacklistDecision(msg Message, meta Meta) (Decision, bool) {
	checks := make([]struct {
		kind BanKind
		id   string
	}, 0, 3)
	if meta.UserID != 0 {
		checks = append(checks, struct {
			kind BanKind
			id   string
		}{BanUser, idString(meta.UserID)})
	}
	if meta.GroupID != 0 {
		checks = append(checks, struct {
			kind BanKind
			id   string
		}{BanGroup, idString(meta.GroupID)})
	}
	if meta.IP != "" {
		checks = append(checks, struct {
			kind BanKind
			id   string
		}{BanIP, meta.IP})
	}
	for _, c := range checks {
		entry, ok := e.bans.Banned(c.kind, c.id)
		if !ok {
			continue
		}
		return Decision{
			Kind:   DecisionBlock,
			Text:   msg.Text,
			Reason: entry.Reason,
			Rule:   "blacklist:" + c.kind.String(),
		}, true
	}
	return Decision{}, false
}

// spamDecision 对用户与群分别做防刷统计，任一超限即返回原因。
func (e *Engine) spamDecision(msg Message, meta Meta) (Decision, bool) {
	subs := make([]struct {
		kind BanKind
		id   string
	}, 0, 2)
	if meta.UserID != 0 {
		subs = append(subs, struct {
			kind BanKind
			id   string
		}{BanUser, idString(meta.UserID)})
	}
	if meta.GroupID != 0 {
		subs = append(subs, struct {
			kind BanKind
			id   string
		}{BanGroup, idString(meta.GroupID)})
	}
	for _, s := range subs {
		reason, text := e.spam.Record(s.id, msg.Text)
		if reason == SpamNone {
			continue
		}
		d := Decision{Kind: DecisionBlock, Text: msg.Text, Reason: text, Rule: "antispam:" + reason.String()}
		if e.bans != nil && e.spam.BanDuration() > 0 {
			if err := e.bans.Ban(s.kind, s.id, text, e.spam.BanDuration()); err != nil {
				e.warnf("moderation: 防刷临时封禁失败: %v", err)
			} else {
				e.count(MetricBanned)
			}
		}
		return d, true
	}
	return Decision{}, false
}

// record 写入审计记录；audit 为 nil 时跳过。
func (e *Engine) record(event string, meta Meta, d Decision) {
	if e.audit == nil {
		return
	}
	e.audit(Record{
		At:      e.now(),
		Event:   event,
		UserID:  meta.UserID,
		GroupID: meta.GroupID,
		Rule:    d.Rule,
		Reason:  d.Reason,
		Score:   d.Score,
		Text:    d.Text,
	})
}

// count 自增指标；metrics 为 nil 时跳过。
func (e *Engine) count(name string) {
	if e.metrics != nil {
		e.metrics.Inc(name)
	}
}

// warnf 在注入告警回调时记录一条告警。
func (e *Engine) warnf(format string, args ...any) {
	if e.warn == nil {
		return
	}
	e.warn(fmt.Sprintf(format, args...))
}

// idString 把整数 ID 转成黑名单键里的字符串形式。
func idString(id int64) string { return strconv.FormatInt(id, 10) }
