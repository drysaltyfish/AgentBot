package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/drysaltyfish/agentbot/internal/scope"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/textsim"
)

// Tier 是分层记忆（F-49）的层级。
type Tier uint8

const (
	// TierWorking 是当前会话最近若干条消息的短期缓冲，先进先出。
	TierWorking Tier = iota
	// TierEpisodic 是按"会话片段"聚合的情节记忆。
	TierEpisodic
	// TierSemantic 是从 Working/Episodic 固化而来的长期事实。
	TierSemantic
)

// String 返回层级名；未知层级返回 "unknown"。
func (t Tier) String() string {
	switch t {
	case TierWorking:
		return "working"
	case TierEpisodic:
		return "episodic"
	case TierSemantic:
		return "semantic"
	default:
		return "unknown"
	}
}

// F-49 的默认参数（与规格一致）。
const (
	// DefaultWorkingLimit 是 Working 的条数上限（也是固化触发阈值）。
	DefaultWorkingLimit = 50
	// DefaultEpisodicLimit 是 Episodic 的片段上限（有界）。
	DefaultEpisodicLimit = 500
	// DefaultSemanticLimit 是 Semantic 的事实条数上限（对齐 F-48 的 200）。
	DefaultSemanticLimit = 200
	// DefaultIdleGap 是开启新会话片段的空闲阈值。
	DefaultIdleGap = 30 * time.Minute
	// DefaultConsolidateTimeout 是单次异步固化的时间预算。
	DefaultConsolidateTimeout = 30 * time.Second
	// DefaultRecallTotal 是三层合并召回的总条数预算。
	DefaultRecallTotal = 10
	// DefaultEpisodeSearchLimit 是按关键词检索片段时最多检查的片段数。
	DefaultEpisodeSearchLimit = 200
)

// 三层召回预算比例：working 1/2 + episodic 1/4 + semantic 1/4。
const (
	DefaultWorkingShare  = 0.5
	DefaultEpisodicShare = 0.25
	DefaultSemanticShare = 0.25
)

// TierItem 是分层记忆中的一条记录。
type TierItem struct {
	ID    int64
	Text  string
	Title string
	Refs  []string
	Tier  Tier
	Score float64
	// SubjectID 是这条记录**关于谁**（群聊里就是写入时的发言人 QQ 号，0 表示未指明）。
	// 作用域决定"谁看得到"，归属人决定"这是谁的事"——两者不同维度（F-47/F-48）。
	// 写入判定只在**同一归属人**之间比较，否则「张三很怕辣」会与「李四很怕辣」合并。
	SubjectID int64
	CreatedAt time.Time
}

// Episode 是一个会话片段：同一段连续对话中的若干条消息。
type Episode struct {
	ID        int64
	Tier      Tier
	Items     []TierItem
	StartedAt time.Time
	EndedAt   time.Time
}

// Consolidator 把一批 Working/Episodic 原文提炼为长期事实（F-49 固化）。
//
// 抽象成接口是为了让"固化是否调用模型"成为可替换的决定，
// 也便于测试注入确定性实现或失败实现。
type Consolidator interface {
	Consolidate(ctx context.Context, scope string, items []TierItem) ([]TierItem, error)
}

// RuleConsolidator 是不调用模型的确定性固化器：把达到分值门槛的条目原样晋升，
// 标题缺失时用正文前 20 字回退（F-48 的降级要求）。
type RuleConsolidator struct {
	// MinScore 是晋升门槛；零值即 0（全部晋升）。
	MinScore float64
	// MaxFacts <= 0 时表示不额外限制单批条数。
	MaxFacts int
}

// Consolidate 实现 Consolidator。
func (r RuleConsolidator) Consolidate(_ context.Context, _ string, items []TierItem) ([]TierItem, error) {
	out := make([]TierItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if it.Score < r.MinScore {
			continue
		}
		key := store.Fingerprint("semantic", it.Text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		fact := cloneTierItem(it)
		fact.Tier = TierSemantic
		if strings.TrimSpace(fact.Title) == "" {
			fact.Title = titleFallback(fact.Text)
		}
		out = append(out, fact)
		if r.MaxFacts > 0 && len(out) >= r.MaxFacts {
			break
		}
	}
	return out, nil
}

// titleFallback 用正文前 20 个字符作为标题（F-48 的标题降级）。
func titleFallback(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 20 {
		runes = runes[:20]
	}
	return string(runes)
}

// RecallPolicy 是三层召回的预算策略。
type RecallPolicy struct {
	// Total 是总条数预算；<=0 时用 DefaultRecallTotal。
	Total int
	// WorkingShare/EpisodicShare/SemanticShare 是各层占比；<=0 时用默认。
	WorkingShare  float64
	EpisodicShare float64
	SemanticShare float64
	// MaxTokens > 0 且 Tokens 非空时，改按 token 配额分配（规格允许）。
	MaxTokens int
	// Tokens 是文本的 token 计量函数。
	Tokens func(string) int
}

// normalized 回填零值，使零值 RecallPolicy 可用。
func (p RecallPolicy) normalized() RecallPolicy {
	if p.Total <= 0 {
		p.Total = DefaultRecallTotal
	}
	if p.WorkingShare <= 0 {
		p.WorkingShare = DefaultWorkingShare
	}
	if p.EpisodicShare <= 0 {
		p.EpisodicShare = DefaultEpisodicShare
	}
	if p.SemanticShare <= 0 {
		p.SemanticShare = DefaultSemanticShare
	}
	return p
}

// TieredOptions 是构造 TieredMemory 的参数。
type TieredOptions struct {
	// Store 是持久层；为 nil 时使用进程内 MemTierStore。
	Store TierStore
	// WorkingLimit/EpisodicLimit/SemanticLimit <= 0 时用对应默认值。
	WorkingLimit  int
	EpisodicLimit int
	SemanticLimit int
	// IdleGap <= 0 时用 DefaultIdleGap。
	IdleGap time.Duration
	// Judge 是可选的语义判官，只在写入落进相似度歧义带时使用；
	// 为 nil 时退回确定性阈值（与 F-87 的 Store 同一套分带口径）。
	Judge Judge
	// DeterministicThreshold <= 0 时用 workingMergeThreshold。
	DeterministicThreshold float64
	// Consolidator 为 nil 时使用 RuleConsolidator。
	Consolidator Consolidator
	// ConsolidateTimeout <= 0 时用 DefaultConsolidateTimeout。
	ConsolidateTimeout time.Duration
	// Clock 为 nil 时用 time.Now；测试可注入确定性时钟。
	Clock func() time.Time
	// Warn 接收降级告警。
	Warn func(string)
	// Recall 是三层召回的预算策略。
	Recall RecallPolicy
}

// TieredMemory 实现 F-49 的分层记忆：Working / Episodic / Semantic 三层。
//
// 它同时满足 agent.Memory 的 Save(ctx, text) / Recall(ctx)，
// 现有调用方无需改动即可替换为分层实现。
//
// 固化是**异步且可失败**的：Save 只做同步的裁剪与片段聚合，
// 真正的提炼在后台执行，失败只告警，绝不影响写入的成功语义。
type TieredMemory struct {
	st                 TierStore
	workingLimit       int
	episodicLimit      int
	semanticLimit      int
	idleGap            time.Duration
	judge              Judge
	threshold          float64
	consolidator       Consolidator
	consolidateTimeout time.Duration
	clock              func() time.Time
	warn               func(string)
	policy             RecallPolicy

	asyncMu            sync.Mutex
	asyncCond          *sync.Cond
	inflight           int
	lastConsolidateErr error
}

// NewTiered 构造分层记忆。
func NewTiered(opts TieredOptions) *TieredMemory {
	m := &TieredMemory{
		st:                 opts.Store,
		workingLimit:       positiveOr(opts.WorkingLimit, DefaultWorkingLimit),
		episodicLimit:      positiveOr(opts.EpisodicLimit, DefaultEpisodicLimit),
		semanticLimit:      positiveOr(opts.SemanticLimit, DefaultSemanticLimit),
		idleGap:            opts.IdleGap,
		judge:              opts.Judge,
		threshold:          opts.DeterministicThreshold,
		consolidator:       opts.Consolidator,
		consolidateTimeout: opts.ConsolidateTimeout,
		clock:              opts.Clock,
		warn:               opts.Warn,
		policy:             opts.Recall,
	}
	if m.st == nil {
		m.st = &MemTierStore{}
	}
	if m.idleGap <= 0 {
		m.idleGap = DefaultIdleGap
	}
	if m.threshold <= 0 {
		m.threshold = workingMergeThreshold
	}
	if m.consolidateTimeout <= 0 {
		m.consolidateTimeout = DefaultConsolidateTimeout
	}
	if m.clock == nil {
		m.clock = time.Now
	}
	if m.consolidator == nil {
		m.consolidator = RuleConsolidator{}
	}
	m.asyncCond = sync.NewCond(&m.asyncMu)
	return m
}

// positiveOr 返回 v（当 v>0），否则返回 def。
func positiveOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// warnf 在配置了 Warn 时记录一条告警。
func (m *TieredMemory) warnf(msg string) {
	if m.warn != nil {
		m.warn(msg)
	}
}

// Save 实现 agent.Memory：把一条事实写入当前作用域的 Working 层。
//
// 写入是**覆盖式**的，不是无脑追加：先在同一个作用域、**同一个归属人**的既有
// Working 条目里找最相似的一条，判为同一件事就地改写（保留 id 与创建顺序），
// 否则才追加。只追加的实现会让"更正"变成两条并存的旧事实，而旧那条同样会被
// 召回——真实事故就是"本人改了称呼，机器人还一直叫旧名字"。
//
// 称呼类事实直接拒绝（见 LooksLikeIdentityFact）：那是平台名片的职责，
// 抄进记忆只会得到一份每轮回灌、且推不翻的过期副本。
//
// 作用域经 ctx 传递（见 internal/scope），与既有 Store 的约定一致。
func (m *TieredMemory) Save(ctx context.Context, text string) error {
	if m == nil || m.st == nil {
		return ErrUnavailable
	}
	trimmed, err := Validate(text)
	if err != nil {
		return err
	}
	if LooksLikeIdentityFact(trimmed) {
		return ErrIdentityFact
	}
	sc := scope.ScopeFrom(ctx)
	if sc == "" {
		// 空作用域会把所有会话混在一起，宁可失败也不串。
		return fmt.Errorf("save tiered memory: %w", ErrUnavailable)
	}
	subject := scope.SubjectFrom(ctx)
	handled, err := m.supersedeWorking(ctx, sc, subject, trimmed)
	if err != nil {
		// 覆盖判定失败不能变成"记忆写不进去"：退回追加，只是留下一次重复。
		m.warnf("tiered memory supersede failed, appending instead: " + err.Error())
	} else if handled {
		return nil
	}
	if _, err := m.st.AppendWorking(ctx, sc, TierItem{Text: trimmed, SubjectID: subject, CreatedAt: m.clock()}); err != nil {
		return err
	}
	m.afterWrite(ctx, sc)
	return nil
}

// supersedeWorking 在**同作用域 + 同归属人**的 Working 条目里找与新事实最相似的一条；
// 判为同一件事时就地改写它（保留 id 与创建顺序），并报告这次写入是否已被吸收。
//
// 就地改写而不是"删旧插新"：Working 的顺序是召回与固化的依据，换成新 id 会让
// 整段顺序位移，也会让正在进行的固化拿到两份内容。
func (m *TieredMemory) supersedeWorking(ctx context.Context, sc string, subject int64, text string) (bool, error) {
	items, err := m.st.Working(ctx, sc)
	if err != nil {
		return false, err
	}
	var (
		bestID   int64
		bestText string
		bestSim  float64
	)
	for _, it := range items {
		if it.SubjectID != subject {
			continue
		}
		if it.Text == text {
			// 完全相同：幂等，忽略这次写入。
			return true, nil
		}
		if sim := textsim.Similarity(text, it.Text); sim > bestSim {
			bestID, bestText, bestSim = it.ID, it.Text, sim
		}
	}
	if bestID == 0 || !m.sameFact(ctx, text, bestText, bestSim) {
		return false, nil
	}
	updated, err := m.st.UpdateWorking(ctx, sc, bestID, TierItem{Text: text, SubjectID: subject})
	if err != nil {
		return false, err
	}
	if !updated {
		// 条目在判定与改写之间被删了（例如用户刚忘了它）：退回追加，别丢这条记忆。
		return false, nil
	}
	return true, nil
}

// workingMergeThreshold 是 Working 层"就地改写"的确定性门槛。
//
// 比 F-87 的 textsim.Threshold(0.50) **更高**，理由是 Working 里同时住着两类东西：
// 真正的长期事实，以及"消息-3""片段消息-7"这类很短的会话片段。文本越短，
// 字符二元组 Jaccard 越容易虚高——"片段消息-0"与"片段消息-1"就有 0.67，
// 但它们是两条不同的片段；用 0.50 会把整个缓冲层合并成一条，固化也就无从触发。
// 门槛定在 0.75：明显是同一件事的改写（如只差一个标点/称谓）仍会命中，
// 灰区交给判官，判官不可用时宁可漏合并（多一条重复，用户看得见）。
const workingMergeThreshold = 0.75

// sameFact 判定新旧两条是否指同一件事。
//
// 分带口径与 F-87 的 Store 相同（>=0.90 并入、<0.30 新增），但门槛更保守：
// 短文本只认"完全相同"或"高度相似"，灰区问判官，判官缺失时按 workingMergeThreshold。
func (m *TieredMemory) sameFact(ctx context.Context, text, existing string, sim float64) bool {
	switch {
	case text == existing:
		return true
	case sim >= HighBand:
		return true
	case sim < LowBand:
		return false
	}
	// 短文本到这里就结束：字符二元组太少，Jaccard 单独不足以说明是同一件事。
	if utf8.RuneCountInString(text) < textsim.MinRunesForSimilarity ||
		utf8.RuneCountInString(existing) < textsim.MinRunesForSimilarity {
		return false
	}
	if m.judge != nil {
		same, err := m.judge.SameFact(ctx, text, existing)
		if err == nil {
			return same
		}
		m.warnf("memory judge unavailable; falling back to the deterministic threshold: " + err.Error())
	}
	return sim >= m.threshold
}

// afterWrite 在 Working 达到上限时裁剪并触发异步固化。
//
// 同步部分只做裁剪与片段聚合；任何失败都只告警，不影响 Save 的成功语义。
func (m *TieredMemory) afterWrite(ctx context.Context, sc string) {
	items, err := m.st.Working(ctx, sc)
	if err != nil {
		m.warnf("tiered memory working read failed: " + err.Error())
		return
	}
	if len(items) < m.workingLimit {
		return
	}
	keep := m.workingLimit / 2
	if keep < 1 {
		keep = 1
	}
	removed, err := m.st.TrimWorking(ctx, sc, keep)
	if err != nil {
		m.warnf("tiered memory trim failed: " + err.Error())
		return
	}
	if len(removed) == 0 {
		return
	}
	if err := m.appendEpisodes(ctx, sc, removed); err != nil {
		m.warnf("tiered memory episode append failed: " + err.Error())
	}
	m.triggerConsolidate(ctx, sc, removed)
}

// appendEpisodes 把被裁掉的旧消息按空闲间隔聚合成片段，并保证片段有界。
func (m *TieredMemory) appendEpisodes(ctx context.Context, sc string, items []TierItem) error {
	for _, ep := range buildEpisodes(items, m.idleGap) {
		if _, err := m.st.AppendEpisode(ctx, sc, ep); err != nil {
			return err
		}
	}
	_, err := m.st.TrimEpisodes(ctx, sc, m.episodicLimit)
	return err
}

// buildEpisodes 按空闲阈值把按时间升序的条目切成片段。
func buildEpisodes(items []TierItem, idleGap time.Duration) []Episode {
	if len(items) == 0 {
		return nil
	}
	var out []Episode
	cur := Episode{
		Items:     []TierItem{cloneTierItem(items[0])},
		StartedAt: items[0].CreatedAt,
		EndedAt:   items[0].CreatedAt,
	}
	for _, it := range items[1:] {
		if idleGap > 0 && it.CreatedAt.Sub(cur.EndedAt) > idleGap {
			out = append(out, cur)
			cur = Episode{
				Items:     []TierItem{cloneTierItem(it)},
				StartedAt: it.CreatedAt,
				EndedAt:   it.CreatedAt,
			}
			continue
		}
		cur.Items = append(cur.Items, cloneTierItem(it))
		cur.EndedAt = it.CreatedAt
	}
	out = append(out, cur)
	return out
}

// triggerConsolidate 启动一次后台固化。它立即返回，固化在独立 goroutine 中执行。
func (m *TieredMemory) triggerConsolidate(ctx context.Context, sc string, items []TierItem) {
	batch := cloneTierItems(items)
	base := context.WithoutCancel(ctx)
	m.asyncMu.Lock()
	m.inflight++
	m.lastConsolidateErr = nil
	m.asyncMu.Unlock()
	go func() {
		err := m.consolidate(base, sc, batch)
		m.asyncMu.Lock()
		m.inflight--
		if err != nil {
			m.lastConsolidateErr = err
		}
		if m.inflight == 0 {
			m.asyncCond.Broadcast()
		}
		m.asyncMu.Unlock()
	}()
}

// consolidate 调用 Consolidator 并把结果写入 Semantic 层。
func (m *TieredMemory) consolidate(ctx context.Context, sc string, items []TierItem) error {
	runCtx, cancel := context.WithTimeout(ctx, m.consolidateTimeout)
	defer cancel()
	facts, err := m.consolidator.Consolidate(runCtx, sc, items)
	if err != nil {
		m.warnf("tiered memory consolidation failed: " + err.Error())
		return err
	}
	for _, f := range facts {
		f.Tier = TierSemantic
		if _, err := m.st.UpsertSemantic(runCtx, sc, f); err != nil {
			m.warnf("tiered memory semantic upsert failed: " + err.Error())
			return err
		}
	}
	if _, err := m.st.TrimSemantics(runCtx, sc, m.semanticLimit); err != nil {
		m.warnf("tiered memory semantic trim failed: " + err.Error())
		return err
	}
	return nil
}

// WaitConsolidation 阻塞到所有在途固化结束，返回最后一次固化的错误。
//
// 测试与需要确定性的调用方用它替代 sleep；ctx 只用于入口的取消检查。
func (m *TieredMemory) WaitConsolidation(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.asyncMu.Lock()
	defer m.asyncMu.Unlock()
	for m.inflight > 0 {
		m.asyncCond.Wait()
	}
	return m.lastConsolidateErr
}

// scopeOf 取出当前作用域；为空时报错。
func (m *TieredMemory) scopeOf(ctx context.Context) (string, error) {
	sc := scope.ScopeFrom(ctx)
	if sc == "" {
		return "", fmt.Errorf("tiered memory: %w", ErrUnavailable)
	}
	return sc, nil
}

// Working 返回当前作用域的 Working 层（按时间升序）。
func (m *TieredMemory) Working(ctx context.Context) ([]TierItem, error) {
	if m == nil || m.st == nil {
		return nil, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	return m.st.Working(ctx, sc)
}

// Episodes 返回当前作用域的片段，按时间升序；limit<=0 表示全部。
func (m *TieredMemory) Episodes(ctx context.Context, limit int) ([]Episode, error) {
	if m == nil || m.st == nil {
		return nil, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	return m.st.Episodes(ctx, sc, limit)
}

// Semantics 返回当前作用域的长期事实（按写入顺序）。
func (m *TieredMemory) Semantics(ctx context.Context) ([]TierItem, error) {
	if m == nil || m.st == nil {
		return nil, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	return m.st.Semantics(ctx, sc)
}

// SearchEpisodes 按关键词与时间区间检索片段。
//
// from/to 为零值表示该端不限；query 为空表示只按时间过滤。
// 结果按开始时间升序，且最多返回 limit 条（limit<=0 表示全部）。
func (m *TieredMemory) SearchEpisodes(ctx context.Context, query string, from, to time.Time, limit int) ([]Episode, error) {
	all, err := m.Episodes(ctx, 0)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	out := make([]Episode, 0, len(all))
	for _, ep := range all {
		if !from.IsZero() && ep.EndedAt.Before(from) {
			continue
		}
		if !to.IsZero() && ep.StartedAt.After(to) {
			continue
		}
		if query != "" {
			matched := false
			for _, it := range ep.Items {
				if lexicalScore(query, it.Text) > 0 {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, ep)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Promote 把一条 Working 或 Episodic 记录晋升为 Semantic 长期事实。
func (m *TieredMemory) Promote(ctx context.Context, id int64) (bool, error) {
	if m == nil || m.st == nil {
		return false, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return false, err
	}
	if it, ok, err := m.findWorking(ctx, sc, id); err != nil {
		return false, err
	} else if ok {
		if _, err := m.st.DeleteWorking(ctx, sc, id); err != nil {
			return false, err
		}
		return m.promoteItem(ctx, sc, it)
	}
	if it, ok, err := m.findEpisodeItem(ctx, sc, id); err != nil {
		return false, err
	} else if ok {
		if _, err := m.st.DeleteEpisodeItem(ctx, sc, id); err != nil {
			return false, err
		}
		return m.promoteItem(ctx, sc, it)
	}
	return false, nil
}

// promoteItem 把一条记录写入 Semantic 并执行淘汰。
func (m *TieredMemory) promoteItem(ctx context.Context, sc string, it TierItem) (bool, error) {
	it.Tier = TierSemantic
	if it.Title == "" {
		it.Title = titleFallback(it.Text)
	}
	if _, err := m.st.UpsertSemantic(ctx, sc, it); err != nil {
		return false, err
	}
	m.trimSemantics(ctx, sc)
	return true, nil
}

// findWorking 在 Working 层按 id 查找。
func (m *TieredMemory) findWorking(ctx context.Context, sc string, id int64) (TierItem, bool, error) {
	items, err := m.st.Working(ctx, sc)
	if err != nil {
		return TierItem{}, false, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, true, nil
		}
	}
	return TierItem{}, false, nil
}

// findEpisodeItem 在 Episodic 层按条目 id 查找。
func (m *TieredMemory) findEpisodeItem(ctx context.Context, sc string, id int64) (TierItem, bool, error) {
	eps, err := m.st.Episodes(ctx, sc, 0)
	if err != nil {
		return TierItem{}, false, err
	}
	for _, ep := range eps {
		for _, it := range ep.Items {
			if it.ID == id {
				return it, true, nil
			}
		}
	}
	return TierItem{}, false, nil
}

// Demote 把一条 Semantic 事实降级为单独一个 Episodic 片段。
func (m *TieredMemory) Demote(ctx context.Context, id int64) (bool, error) {
	if m == nil || m.st == nil {
		return false, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return false, err
	}
	items, err := m.st.Semantics(ctx, sc)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if it.ID != id {
			continue
		}
		if _, err := m.st.DeleteSemantic(ctx, sc, id); err != nil {
			return false, err
		}
		it.Tier = TierEpisodic
		ep := Episode{
			Items:     []TierItem{it},
			StartedAt: it.CreatedAt,
			EndedAt:   it.CreatedAt,
		}
		if _, err := m.st.AppendEpisode(ctx, sc, ep); err != nil {
			return false, err
		}
		if _, err := m.st.TrimEpisodes(ctx, sc, m.episodicLimit); err != nil {
			m.warnf("tiered memory episode trim failed: " + err.Error())
		}
		return true, nil
	}
	return false, nil
}

// trimSemantics 把 Semantic 压到配置上限；失败只告警。
func (m *TieredMemory) trimSemantics(ctx context.Context, sc string) {
	if _, err := m.st.TrimSemantics(ctx, sc, m.semanticLimit); err != nil {
		m.warnf("tiered memory semantic trim failed: " + err.Error())
	}
}

// Recall 实现 agent.Memory：返回三层合并、去重、按相关度重排后的文本。
func (m *TieredMemory) Recall(ctx context.Context) ([]string, error) {
	items, err := m.RecallLayered(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out, nil
}

// RecallLayered 按预算从三层召回，合并去重并按相关度降序返回。
//
// query 为空时相关度即层级权重，排序退化为"语义 > 情节 > 工作、再按时间新到旧"。
func (m *TieredMemory) RecallLayered(ctx context.Context, query string) ([]TierItem, error) {
	if m == nil || m.st == nil {
		return nil, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	policy := m.policy.normalized()
	counts, tokenBudgets := policy.quotas()

	working, err := m.st.Working(ctx, sc)
	if err != nil {
		return nil, err
	}
	picked := takeNewest(working, counts[0], tokenBudgets[0], policy.Tokens)

	if counts[1] > 0 || tokenBudgets[1] > 0 {
		eps, err := m.st.Episodes(ctx, sc, DefaultEpisodeSearchLimit)
		if err != nil {
			return nil, err
		}
		picked = append(picked, selectEpisodeItems(eps, query, counts[1], tokenBudgets[1], policy.Tokens)...)
	}
	if counts[2] > 0 || tokenBudgets[2] > 0 {
		facts, err := m.st.Semantics(ctx, sc)
		if err != nil {
			return nil, err
		}
		picked = append(picked, selectItems(facts, query, counts[2], tokenBudgets[2], policy.Tokens)...)
	}
	return rankAndDedupe(picked, query), nil
}

// Forget 删除一条记忆（F-88 的 forget_memory）。
//
// 顺序是刻意的：先在长期事实里找（那正是用户通过 list_memories 看到的东西），
// 找不到再退到 Working / Episodic。反过来的话会出现"列表里有、删不掉"——
// 那是最让人困惑的一类不一致。
func (m *TieredMemory) Forget(ctx context.Context, id int64) (bool, error) {
	if m == nil || m.st == nil {
		return false, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return false, err
	}
	if src, ok := m.st.(MemoryAdminSource); ok {
		deleted, ferr := src.ForgetMemory(ctx, sc, id)
		if ferr != nil {
			return false, ferr
		}
		if deleted {
			return true, nil
		}
	}
	if ok, werr := m.st.DeleteWorking(ctx, sc, id); werr != nil {
		return false, werr
	} else if ok {
		return true, nil
	}
	return m.st.DeleteEpisodeItem(ctx, sc, id)
}

// ForgetScope 清空当前作用域的三层（F-88）。
//
// TierStore 保持最小：它没有"清空作用域"接口，因此过程层按条删除。
// 删除失败不静默：能删多少返回多少，但第一个错误会带出去。
func (m *TieredMemory) ForgetScope(ctx context.Context) (int, error) {
	if m == nil || m.st == nil {
		return 0, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	if src, ok := m.st.(MemoryAdminSource); ok {
		n, ferr := src.ForgetScope(ctx, sc)
		if ferr != nil {
			return total, ferr
		}
		total += n
	}
	working, werr := m.st.Working(ctx, sc)
	if werr != nil {
		return total, werr
	}
	for _, it := range working {
		if ok, derr := m.st.DeleteWorking(ctx, sc, it.ID); derr != nil {
			return total, derr
		} else if ok {
			total++
		}
	}
	eps, eerr := m.st.Episodes(ctx, sc, 0)
	if eerr != nil {
		return total, eerr
	}
	for _, ep := range eps {
		if ok, derr := m.st.DeleteEpisode(ctx, sc, ep.ID); derr != nil {
			return total, derr
		} else if ok {
			total += len(ep.Items)
		}
	}
	return total, nil
}

// List 列出当前作用域的长期事实（F-88 的 list_memories）。
//
// 只列长期事实，不列 Working/Episodic：用户说"列出我的记忆"指的是被固化下来的
// 东西；把会话缓冲也列出来会让列表在每次对话后剧烈变化，等于没有列表。
func (m *TieredMemory) List(ctx context.Context, limit int) ([]store.Memory, error) {
	if m == nil || m.st == nil {
		return nil, ErrUnavailable
	}
	sc, err := m.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if src, ok := m.st.(MemoryAdminSource); ok {
		return src.ListMemories(ctx, sc, limit)
	}
	// 语义层给不出完整视图时，退化为从 TierItem 拼一个最小视图（字段会缺，
	// 但绝不返回伪造的作用域或时间戳）。
	items, serr := m.st.Semantics(ctx, sc)
	if serr != nil {
		return nil, serr
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]store.Memory, 0, len(items))
	for _, it := range items {
		out = append(out, store.Memory{
			ID:        it.ID,
			ScopeKey:  sc,
			Title:     it.Title,
			Text:      it.Text,
			Score:     it.Score,
			CreatedAt: it.CreatedAt.UnixMilli(),
		})
	}
	return out, nil
}

// quotas 返回三层的条数配额与 token 配额（下标 0/1/2 对应 Working/Episodic/Semantic）。
func (p RecallPolicy) quotas() ([3]int, [3]int) {
	var counts, tokens [3]int
	if p.MaxTokens > 0 && p.Tokens != nil {
		tokens[0] = int(float64(p.MaxTokens) * p.WorkingShare)
		tokens[1] = int(float64(p.MaxTokens) * p.EpisodicShare)
		tokens[2] = int(float64(p.MaxTokens) * p.SemanticShare)
		counts = [3]int{p.Total, p.Total, p.Total}
		return counts, tokens
	}
	counts[0] = int(float64(p.Total) * p.WorkingShare)
	counts[1] = int(float64(p.Total) * p.EpisodicShare)
	counts[2] = int(float64(p.Total) * p.SemanticShare)
	return counts, tokens
}

// takeNewest 从最新一端按预算取条目。
func takeNewest(items []TierItem, count, tokenBudget int, tokens func(string) int) []TierItem {
	if count <= 0 && tokenBudget <= 0 {
		return nil
	}
	out := make([]TierItem, 0, min(count, len(items)))
	used := 0
	for i := len(items) - 1; i >= 0; i-- {
		if count > 0 && len(out) >= count {
			break
		}
		it := items[i]
		if tokenBudget > 0 {
			cost := tokenCost(it.Text, tokens)
			if used+cost > tokenBudget {
				continue
			}
			used += cost
		}
		out = append(out, cloneTierItem(it))
	}
	return out
}

// selectItems 按相关度（query 为空则按时间）从候选里取预算内的条目。
func selectItems(items []TierItem, query string, count, tokenBudget int, tokens func(string) int) []TierItem {
	if count <= 0 && tokenBudget <= 0 {
		return nil
	}
	candidates := cloneTierItems(items)
	sort.SliceStable(candidates, func(a, b int) bool {
		if query != "" {
			sa, sb := lexicalScore(query, candidates[a].Text), lexicalScore(query, candidates[b].Text)
			if sa != sb {
				return sa > sb
			}
		}
		return candidates[a].CreatedAt.After(candidates[b].CreatedAt)
	})
	return takeBudget(candidates, count, tokenBudget, tokens)
}

// selectEpisodeItems 把片段摊平成条目后按 selectItems 的规则取预算。
func selectEpisodeItems(eps []Episode, query string, count, tokenBudget int, tokens func(string) int) []TierItem {
	if count <= 0 && tokenBudget <= 0 {
		return nil
	}
	var all []TierItem
	for _, ep := range eps {
		all = append(all, ep.Items...)
	}
	return selectItems(all, query, count, tokenBudget, tokens)
}

// takeBudget 顺序取预算内的条目。
func takeBudget(items []TierItem, count, tokenBudget int, tokens func(string) int) []TierItem {
	out := make([]TierItem, 0, min(count, len(items)))
	used := 0
	for _, it := range items {
		if count > 0 && len(out) >= count {
			break
		}
		if tokenBudget > 0 {
			cost := tokenCost(it.Text, tokens)
			if used+cost > tokenBudget {
				continue
			}
			used += cost
		}
		out = append(out, cloneTierItem(it))
	}
	return out
}

// tokenCost 返回文本的 token 数；计量函数缺失时退化为字符数。
func tokenCost(text string, tokens func(string) int) int {
	if tokens != nil {
		return tokens(text)
	}
	return len([]rune(text))
}

// rankAndDedupe 按文本指纹去重，并按相关度降序、时间降序、ID 升序重排。
func rankAndDedupe(items []TierItem, query string) []TierItem {
	best := make(map[string]TierItem, len(items))
	for _, it := range items {
		key := store.Fingerprint("tier-item", it.Text)
		prev, ok := best[key]
		if !ok {
			best[key] = it
			continue
		}
		if keepCandidate(it, prev, query) {
			best[key] = it
		}
	}
	out := make([]TierItem, 0, len(best))
	for _, it := range best {
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool {
		ra, rb := relevance(out[a], query), relevance(out[b], query)
		if ra != rb {
			return ra > rb
		}
		if !out[a].CreatedAt.Equal(out[b].CreatedAt) {
			return out[a].CreatedAt.After(out[b].CreatedAt)
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// keepCandidate 决定重复文本里保留哪一条：先比相关度，再比层级优先级。
func keepCandidate(newer, older TierItem, query string) bool {
	rn, ro := relevance(newer, query), relevance(older, query)
	if rn != ro {
		return rn > ro
	}
	return tierPriority(newer.Tier) > tierPriority(older.Tier)
}

// tierPriority 返回层级优先级：语义 > 情节 > 工作。
func tierPriority(t Tier) int {
	switch t {
	case TierSemantic:
		return 3
	case TierEpisodic:
		return 2
	case TierWorking:
		return 1
	default:
		return 0
	}
}

// relevance 是条目对查询的相关度；query 为空时只保留层级权重。
func relevance(it TierItem, query string) float64 {
	base := 0.0
	if strings.TrimSpace(query) != "" {
		base = lexicalScore(query, it.Text)
	}
	return base + float64(tierPriority(it.Tier))*0.1
}

// lexicalScore 返回 query 的 token 在 text 中命中的比例（0~1）。
func lexicalScore(query, text string) float64 {
	qt := uniqueTokens(tokenize(query))
	if len(qt) == 0 {
		return 0
	}
	set := make(map[string]struct{})
	for _, t := range tokenize(text) {
		set[t] = struct{}{}
	}
	hit := 0
	for _, t := range qt {
		if _, ok := set[t]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(qt))
}
