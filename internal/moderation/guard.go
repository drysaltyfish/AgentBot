package moderation

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 本文件里的 GuardChain / RuleGuard / LLMGuard / VectorGuard 都是**完整实现但当前无消费方**：
// 组合根构造 moderation.Engine 时没有填 Options.Guards（见 HANDOFF「已知偏离」），
// 所以它们在生产里一次都不会跑。线上生效的入站审查是黑名单 → 防刷 → 敏感词自动机。
//
// 接线的前提是先有配置面：规则表（RuleGuard 的 []Rule）、分类器实现（LLMGuard 需要
// 一个会花钱的模型调用）、恶意语料索引（VectorGuard 复用 F-50）。那是新特性，不是接线。
// 改这些实现不会影响线上行为。

// Verdict 是单个 InboundGuard 的判定结果。
type Verdict struct {
	// Allow 为 false 表示拒绝。
	Allow bool
	// Reason 是拒绝原因（允许时可为空）。
	Reason string
	// Score 是命中评分，供调参与审计。
	Score float64
	// Rule 是命中规则名。
	Rule string
	// Text 是 guard 建议使用的文本（通常为空，表示不改写）。
	Text string
}

// InboundGuard 审查一条入站消息。实现必须尊重 ctx：超时后应尽快返回。
type InboundGuard interface {
	Check(ctx context.Context, msg Message, meta Meta) (*Verdict, error)
}

// GuardFunc 让普通函数满足 InboundGuard；nil 函数恒放行。
type GuardFunc func(ctx context.Context, msg Message, meta Meta) (*Verdict, error)

// Check 实现 InboundGuard。
func (f GuardFunc) Check(ctx context.Context, msg Message, meta Meta) (*Verdict, error) {
	if f == nil {
		return &Verdict{Allow: true}, nil
	}
	return f(ctx, msg, meta)
}

// DefaultGuardTimeout 是单个 guard 的默认超时。
const DefaultGuardTimeout = 200 * time.Millisecond

// GuardOptions 控制 GuardChain 的超时与失败策略。
type GuardOptions struct {
	// Timeout 是单个 guard 的超时，<=0 时取 DefaultGuardTimeout。
	Timeout time.Duration
	// FailClosed 为 true 时，guard 超时/出错按拒绝处理；默认 false（fail-open）。
	FailClosed bool
	// Warn 接收超时/出错告警，可为 nil。
	Warn func(string)
}

// GuardChain 按顺序执行 guard，任一拒绝即短路返回。
//
// 单个 guard 超时或返回 error 时按 GuardOptions.FailClosed 决定放行或拒绝；
// guard panic 不会传播到调用方，而是同样按失败策略处理（默认放行）。
type GuardChain struct {
	guards []InboundGuard
	opts   GuardOptions
}

// NewGuardChain 构造 guard 链并复制传入切片。
func NewGuardChain(opts GuardOptions, guards ...InboundGuard) *GuardChain {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultGuardTimeout
	}
	return &GuardChain{guards: append([]InboundGuard(nil), guards...), opts: opts}
}

// Guards 返回 guard 列表的副本。
func (c *GuardChain) Guards() []InboundGuard {
	if c == nil {
		return nil
	}
	return append([]InboundGuard(nil), c.guards...)
}

// Check 依次执行 guard，返回第一个拒绝结论；全部通过时返回 Allow。
func (c *GuardChain) Check(ctx context.Context, msg Message, meta Meta) (*Verdict, error) {
	if c == nil {
		return &Verdict{Allow: true}, nil
	}
	for _, g := range c.guards {
		if g == nil {
			continue
		}
		v, err := c.checkOne(ctx, g, msg, meta)
		if err != nil {
			// 超时或出错：按配置 fail-open / fail-closed。
			if c.opts.FailClosed {
				return &Verdict{Allow: false, Reason: "审查不可用（fail-closed）: " + err.Error(), Rule: "guard"}, nil
			}
			c.warnf("moderation: guard 失败，按 fail-open 放行: %v", err)
			continue
		}
		if v != nil && !v.Allow {
			return v, nil
		}
	}
	return &Verdict{Allow: true}, nil
}

// checkOne 在超时上下文中执行单个 guard，并兜底 panic。
func (c *GuardChain) checkOne(ctx context.Context, g InboundGuard, msg Message, meta Meta) (v *Verdict, err error) {
	runCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			v = nil
			err = fmt.Errorf("guard panic: %v", r)
		}
	}()
	v, err = g.Check(runCtx, msg, meta)
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
	}
	return v, err
}

// warnf 在注入告警回调时记录一条告警。
func (c *GuardChain) warnf(format string, args ...any) {
	if c.opts.Warn == nil {
		return
	}
	c.opts.Warn(fmt.Sprintf(format, args...))
}

// Rule 是一条关键词/正则审查规则；Regex 与 Keyword 至少填一个。
type Rule struct {
	// Name 是规则名（审计展示），为空时自动编号。
	Name string
	// Regex 是正则表达式；与 Keyword 同时填写时正则优先。
	Regex string
	// Keyword 是大小写不敏感的关键词子串。
	Keyword string
	// Reason 是拒绝原因，为空时按规则名生成。
	Reason string
	// Score 是命中评分，<=0 时取 1。
	Score float64
}

// compiledRule 是 Rule 的编译形态。
type compiledRule struct {
	name    string
	reason  string
	score   float64
	re      *regexp.Regexp
	keyword string
}

// RuleGuard 按关键词/正则规则审查（F-57 内置 RuleGuard）。
type RuleGuard struct {
	rules     []compiledRule
	threshold float64
}

// NewRuleGuard 编译规则；正则非法或规则两者皆空时返回 error。
//
// threshold > 0 时只有最高评分达到阈值才拒绝，否则任一命中即拒绝。
func NewRuleGuard(rules []Rule, threshold float64) (*RuleGuard, error) {
	g := &RuleGuard{threshold: threshold}
	for i, r := range rules {
		if r.Regex == "" && r.Keyword == "" {
			return nil, fmt.Errorf("moderation: 规则 %d 必须填写 Regex 或 Keyword", i)
		}
		cr := compiledRule{name: r.Name, reason: r.Reason, score: r.Score}
		if cr.name == "" {
			cr.name = fmt.Sprintf("rule-%d", i)
		}
		if cr.reason == "" {
			cr.reason = "命中规则 " + cr.name
		}
		if cr.score <= 0 {
			cr.score = 1
		}
		if r.Regex != "" {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return nil, fmt.Errorf("moderation: 规则 %q 正则非法: %w", cr.name, err)
			}
			cr.re = re
		}
		if r.Keyword != "" {
			cr.keyword = strings.ToLower(r.Keyword)
		}
		g.rules = append(g.rules, cr)
	}
	return g, nil
}

// Check 实现 InboundGuard。
func (g *RuleGuard) Check(_ context.Context, msg Message, _ Meta) (*Verdict, error) {
	if g == nil {
		return &Verdict{Allow: true}, nil
	}
	lower := strings.ToLower(msg.Text)
	best := 0.0
	hit := -1
	for i := range g.rules {
		r := &g.rules[i]
		matched := false
		if r.re != nil {
			matched = r.re.MatchString(msg.Text)
		}
		if !matched && r.keyword != "" {
			matched = strings.Contains(lower, r.keyword)
		}
		if matched && r.score > best {
			best = r.score
			hit = i
		}
	}
	if hit < 0 || (g.threshold > 0 && best < g.threshold) {
		return &Verdict{Allow: true, Score: best}, nil
	}
	r := &g.rules[hit]
	return &Verdict{Allow: false, Reason: r.reason, Rule: r.name, Score: best}, nil
}

// ClassifyResult 是小模型分类的返回。
type ClassifyResult struct {
	// Score 是恶意概率/评分，越大越可疑。
	Score float64
	// Reason 是分类理由。
	Reason string
	// Tokens 是本次调用消耗的 token 数（用于 F-66 成本统计）。
	Tokens int
}

// Classifier 是小模型分类器抽象；实现必须尊重 ctx 超时。
type Classifier interface {
	Classify(ctx context.Context, text string) (ClassifyResult, error)
}

// LLMOptions 控制 LLMGuard。
type LLMOptions struct {
	// Threshold 是判定阈值，<=0 时取 0.8。
	Threshold float64
	// Cost 在每次成功分类后按 token 数回调，可为 nil（F-66 成本统计）。
	Cost func(tokens int)
}

// LLMGuard 调用小模型做分类（F-57 内置 LLMGuard）。
//
// 超时由外层的 GuardChain 统一控制；本 guard 只负责换算评分与计费。
type LLMGuard struct {
	classifier Classifier
	threshold  float64
	cost       func(tokens int)
}

// NewLLMGuard 构造 LLMGuard。
func NewLLMGuard(c Classifier, opts LLMOptions) *LLMGuard {
	if opts.Threshold <= 0 {
		opts.Threshold = 0.8
	}
	return &LLMGuard{classifier: c, threshold: opts.Threshold, cost: opts.Cost}
}

// Check 实现 InboundGuard。
func (g *LLMGuard) Check(ctx context.Context, msg Message, _ Meta) (*Verdict, error) {
	if g == nil || g.classifier == nil {
		return &Verdict{Allow: true}, nil
	}
	res, err := g.classifier.Classify(ctx, msg.Text)
	if err != nil {
		return nil, err
	}
	if g.cost != nil && res.Tokens > 0 {
		g.cost(res.Tokens)
	}
	if res.Score >= g.threshold {
		reason := res.Reason
		if reason == "" {
			reason = fmt.Sprintf("LLM 分类评分 %.3f ≥ %.3f", res.Score, g.threshold)
		}
		return &Verdict{Allow: false, Reason: reason, Rule: "llm", Score: res.Score}, nil
	}
	return &Verdict{Allow: true, Score: res.Score, Rule: "llm"}, nil
}

// VectorClassifier 与已知恶意语料做相似度比对（复用 F-50）。
type VectorClassifier interface {
	Similarity(ctx context.Context, text string) (float64, error)
}

// VectorGuard 用相似度阈值拦截已知恶意语料（F-57 内置 VectorGuard）。
type VectorGuard struct {
	vc        VectorClassifier
	threshold float64
}

// NewVectorGuard 构造 VectorGuard；threshold <= 0 时取 0.9。
func NewVectorGuard(vc VectorClassifier, threshold float64) *VectorGuard {
	if threshold <= 0 {
		threshold = 0.9
	}
	return &VectorGuard{vc: vc, threshold: threshold}
}

// Check 实现 InboundGuard。
func (g *VectorGuard) Check(ctx context.Context, msg Message, _ Meta) (*Verdict, error) {
	if g == nil || g.vc == nil {
		return &Verdict{Allow: true}, nil
	}
	sim, err := g.vc.Similarity(ctx, msg.Text)
	if err != nil {
		return nil, err
	}
	if sim >= g.threshold {
		return &Verdict{
			Allow:  false,
			Reason: fmt.Sprintf("与已知恶意语料相似度 %.3f ≥ %.3f", sim, g.threshold),
			Rule:   "vector",
			Score:  sim,
		}, nil
	}
	return &Verdict{Allow: true, Score: sim, Rule: "vector"}, nil
}
