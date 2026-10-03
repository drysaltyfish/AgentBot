package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// f49Clock 返回一个确定性时钟，每次调用前进 step。
func f49Clock(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		v := cur
		cur = cur.Add(step)
		return v
	}
}

// failingConsolidator 固定返回错误，用于验证"固化失败不影响主流程"。
type failingConsolidator struct{ err error }

func (f failingConsolidator) Consolidate(context.Context, string, []TierItem) ([]TierItem, error) {
	return nil, f.err
}

// warnLog 并发安全地收集告警。
type warnLog struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnLog) add(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msg)
}

func (w *warnLog) joined() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.msgs, "\n")
}

// Test_F49_TierString 覆盖层级枚举的字符串化与未知值兜底。
func Test_F49_TierString(t *testing.T) {
	t.Parallel()
	cases := map[Tier]string{TierWorking: "working", TierEpisodic: "episodic", TierSemantic: "semantic"}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Fatalf("Tier(%d).String() = %q，期望 %q", tier, got, want)
		}
	}
	if Tier(99).String() != "unknown" {
		t.Fatalf("未知层级应返回 unknown")
	}
}

// Test_F49_WorkingTrimmedEpisodicAndSemanticGenerated 是规格的核心验收：
// 注入 100 条消息后 Working 被裁剪、Episodic 生成片段、Semantic 有固化条目。
func Test_F49_WorkingTrimmedEpisodicAndSemanticGenerated(t *testing.T) {
	t.Parallel()
	warns := &warnLog{}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tm := NewTiered(TieredOptions{
		Clock: f49Clock(start, time.Minute),
		Warn:  warns.add,
	})
	ctx := ctxScope("g")
	for i := 0; i < 100; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("消息-%d", i)); err != nil {
			t.Fatalf("Save(%d): %v", i, err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err != nil {
		t.Fatalf("WaitConsolidation: %v", err)
	}
	working, err := tm.Working(ctx)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(working) == 0 || len(working) > DefaultWorkingLimit {
		t.Fatalf("Working 应有界且非空，得到 %d", len(working))
	}
	eps, err := tm.Episodes(ctx, 0)
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(eps) == 0 {
		t.Fatalf("Episodic 应生成片段")
	}
	facts, err := tm.Semantics(ctx)
	if err != nil {
		t.Fatalf("Semantics: %v", err)
	}
	if len(facts) == 0 {
		t.Fatalf("Semantic 应有固化条目")
	}
	for _, f := range facts {
		if f.Tier != TierSemantic {
			t.Fatalf("固化条目层级应为 semantic，得到 %v", f.Tier)
		}
	}
	for _, ep := range eps {
		for _, it := range ep.Items {
			if it.Tier != TierEpisodic {
				t.Fatalf("片段条目层级应为 episodic，得到 %v", it.Tier)
			}
		}
	}
	if warns.joined() != "" {
		t.Fatalf("成功路径不应有告警: %s", warns.joined())
	}
}

// Test_F49_EpisodesSplitByIdleGap 验证空闲阈值开启新片段。
func Test_F49_EpisodesSplitByIdleGap(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	tm := NewTiered(TieredOptions{
		WorkingLimit: 4,
		IdleGap:      30 * time.Minute,
		Clock:        f49Clock(start, time.Hour),
	})
	ctx := ctxScope("g")
	for i := 0; i < 4; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("片段消息-%d", i)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err != nil {
		t.Fatalf("WaitConsolidation: %v", err)
	}
	eps, err := tm.Episodes(ctx, 0)
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(eps) < 2 {
		t.Fatalf("每条消息相隔 1 小时，应切成至少 2 个片段，得到 %d", len(eps))
	}
	for _, ep := range eps {
		if len(ep.Items) != 1 {
			t.Fatalf("本用例每条消息独立成段，得到 %d 条", len(ep.Items))
		}
	}
}

// Test_F49_ConsolidationFailureDoesNotAffectSave 覆盖"固化失败只告警"。
func Test_F49_ConsolidationFailureDoesNotAffectSave(t *testing.T) {
	t.Parallel()
	warns := &warnLog{}
	tm := NewTiered(TieredOptions{
		WorkingLimit: 4,
		Consolidator: failingConsolidator{err: errors.New("模型不可用")},
		Warn:         warns.add,
	})
	ctx := ctxScope("g")
	for i := 0; i < 4; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("事实-%d", i)); err != nil {
			t.Fatalf("固化失败不得让 Save 失败: %v", err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err == nil {
		t.Fatalf("WaitConsolidation 应报告固化失败")
	}
	if !strings.Contains(warns.joined(), "consolidation failed") {
		t.Fatalf("应记录固化失败告警: %s", warns.joined())
	}
	// 主流程数据仍在：Working 已裁剪，Episodic 已生成。
	eps, err := tm.Episodes(ctx, 0)
	if err != nil || len(eps) == 0 {
		t.Fatalf("固化失败不影响片段生成: eps=%d err=%v", len(eps), err)
	}
	facts, err := tm.Semantics(ctx)
	if err != nil {
		t.Fatalf("Semantics: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("固化失败不应写入 Semantic: %d", len(facts))
	}
}

// Test_F49_RecallBudgetDedupAndOrder 验证预算、去重与相关度重排。
func Test_F49_RecallBudgetDedupAndOrder(t *testing.T) {
	t.Parallel()
	st := &MemTierStore{}
	tm := NewTiered(TieredOptions{Store: st, Recall: RecallPolicy{Total: 4}})
	ctx := ctxScope("g")
	for i := 0; i < 10; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("工作记忆-%d", i)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// 把一条已有文本放进 Semantic，制造跨层重复。
	if _, err := st.UpsertSemantic(ctx, "g", TierItem{Text: "工作记忆-9", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("UpsertSemantic: %v", err)
	}
	items, err := tm.RecallLayered(ctx, "")
	if err != nil {
		t.Fatalf("RecallLayered: %v", err)
	}
	if len(items) > 4 {
		t.Fatalf("召回不得超过总预算 4，得到 %d", len(items))
	}
	dup := 0
	for _, it := range items {
		if it.Text == "工作记忆-9" {
			dup++
		}
	}
	if dup != 1 {
		t.Fatalf("跨层重复应去重为 1 条，得到 %d", dup)
	}
	// 相关度必须降序（同层内层级权重相同，至少保证整体不升）。
	for i := 1; i < len(items); i++ {
		if relevance(items[i-1], "") < relevance(items[i], "") {
			t.Fatalf("相关度必须降序: %+v", items)
		}
	}
}

// Test_F49_PromoteAndDemote 覆盖层级的晋升与降级。
func Test_F49_PromoteAndDemote(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{})
	ctx := ctxScope("g")
	if err := tm.Save(ctx, "主人喜欢橘子味"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	working, _ := tm.Working(ctx)
	if len(working) != 1 {
		t.Fatalf("应有 1 条 Working，得到 %d", len(working))
	}
	id := working[0].ID

	if ok, err := tm.Promote(ctx, id); err != nil || !ok {
		t.Fatalf("Promote 应成功: ok=%v err=%v", ok, err)
	}
	if got, _ := tm.Working(ctx); len(got) != 0 {
		t.Fatalf("晋升后 Working 应为空: %+v", got)
	}
	facts, _ := tm.Semantics(ctx)
	if len(facts) != 1 || facts[0].Text != "主人喜欢橘子味" || facts[0].Tier != TierSemantic {
		t.Fatalf("晋升后 Semantic 应含该条: %+v", facts)
	}

	if ok, err := tm.Demote(ctx, facts[0].ID); err != nil || !ok {
		t.Fatalf("Demote 应成功: ok=%v err=%v", ok, err)
	}
	if got, _ := tm.Semantics(ctx); len(got) != 0 {
		t.Fatalf("降级后 Semantic 应为空: %+v", got)
	}
	eps, _ := tm.Episodes(ctx, 0)
	if len(eps) != 1 || eps[0].Items[0].Text != "主人喜欢橘子味" || eps[0].Items[0].Tier != TierEpisodic {
		t.Fatalf("降级后 Episodic 应含该条: %+v", eps)
	}

	if ok, err := tm.Promote(ctx, 99999); err != nil || ok {
		t.Fatalf("晋升不存在的 id 应返回 false: ok=%v err=%v", ok, err)
	}
	if ok, err := tm.Demote(ctx, 99999); err != nil || ok {
		t.Fatalf("降级不存在的 id 应返回 false: ok=%v err=%v", ok, err)
	}
}

// Test_F49_ScopeIsolation 验证三层都严格按作用域隔离。
func Test_F49_ScopeIsolation(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{WorkingLimit: 2})
	a, b := ctxScope("群A"), ctxScope("群B")
	for i := 0; i < 4; i++ {
		if err := tm.Save(a, fmt.Sprintf("群的秘密-%d", i)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err != nil {
		t.Fatalf("WaitConsolidation: %v", err)
	}
	gotA, err := tm.RecallLayered(a, "秘密")
	if err != nil {
		t.Fatalf("RecallLayered A: %v", err)
	}
	if len(gotA) == 0 {
		t.Fatalf("群 A 应能召回自己的记忆")
	}
	gotB, err := tm.RecallLayered(b, "秘密")
	if err != nil {
		t.Fatalf("RecallLayered B: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("群 B 不得看到群 A 的记忆: %+v", gotB)
	}
	if facts, _ := tm.Semantics(b); len(facts) != 0 {
		t.Fatalf("群 B 的 Semantic 应为空: %+v", facts)
	}
}

// Test_F49_SearchEpisodesByKeywordAndTime 覆盖片段的关键词与时间检索。
func Test_F49_SearchEpisodesByKeywordAndTime(t *testing.T) {
	t.Parallel()
	st := &MemTierStore{}
	tm := NewTiered(TieredOptions{Store: st})
	ctx := ctxScope("g")
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	later := base.Add(2 * time.Hour)
	if _, err := st.AppendEpisode(ctx, "g", Episode{
		Items:     []TierItem{{Text: "主人喜欢猫", CreatedAt: base}},
		StartedAt: base, EndedAt: base,
	}); err != nil {
		t.Fatalf("AppendEpisode: %v", err)
	}
	if _, err := st.AppendEpisode(ctx, "g", Episode{
		Items:     []TierItem{{Text: "主人喜欢狗", CreatedAt: later}},
		StartedAt: later, EndedAt: later,
	}); err != nil {
		t.Fatalf("AppendEpisode: %v", err)
	}

	byKeyword, err := tm.SearchEpisodes(ctx, "猫", time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("SearchEpisodes: %v", err)
	}
	if len(byKeyword) != 1 || byKeyword[0].Items[0].Text != "主人喜欢猫" {
		t.Fatalf("关键词检索应命中猫片段: %+v", byKeyword)
	}
	byTime, err := tm.SearchEpisodes(ctx, "", time.Time{}, base.Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("SearchEpisodes: %v", err)
	}
	if len(byTime) != 1 || byTime[0].Items[0].Text != "主人喜欢猫" {
		t.Fatalf("时间过滤应只留下更早的片段: %+v", byTime)
	}
}

// Test_F49_LayersAreBounded 验证各层容量上限有界。
func Test_F49_LayersAreBounded(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{
		WorkingLimit:  4,
		EpisodicLimit: 2,
		SemanticLimit: 3,
		Clock:         f49Clock(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Minute),
	})
	ctx := ctxScope("g")
	for i := 0; i < 100; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("有界消息-%d", i)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err != nil {
		t.Fatalf("WaitConsolidation: %v", err)
	}
	working, _ := tm.Working(ctx)
	if len(working) > 4 {
		t.Fatalf("Working 超限: %d", len(working))
	}
	eps, _ := tm.Episodes(ctx, 0)
	if len(eps) > 2 {
		t.Fatalf("Episodic 超限: %d", len(eps))
	}
	facts, _ := tm.Semantics(ctx)
	if len(facts) > 3 {
		t.Fatalf("Semantic 超限: %d", len(facts))
	}
}

// Test_F49_RecallTokenBudget 覆盖"配额可按 token 计"。
func Test_F49_RecallTokenBudget(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{
		Recall: RecallPolicy{
			Total:     20,
			MaxTokens: 40,
			Tokens:    func(s string) int { return len([]rune(s)) },
		},
	})
	ctx := ctxScope("g")
	for i := 0; i < 8; i++ {
		if err := tm.Save(ctx, fmt.Sprintf("记忆文本-%d", i)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	items, err := tm.RecallLayered(ctx, "")
	if err != nil {
		t.Fatalf("RecallLayered: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("token 预算下也应召回结果")
	}
	used := 0
	for _, it := range items {
		used += len([]rune(it.Text))
	}
	if used > 40 {
		t.Fatalf("token 用量超过预算 40: %d", used)
	}
}

// Test_F49_SaveValidatesAndRequiresScope 守住与既有 Store 一致的校验与作用域要求。
func Test_F49_SaveValidatesAndRequiresScope(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{})
	if err := tm.Save(context.Background(), "x"); err == nil {
		t.Fatalf("无作用域必须报错")
	}
	if err := tm.Save(ctxScope("g"), "   "); !errors.Is(err, ErrEmpty) {
		t.Fatalf("空内容应报 ErrEmpty: %v", err)
	}
	if err := tm.Save(ctxScope("g"), strings.Repeat("字", Limit+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("超长应报 ErrTooLong: %v", err)
	}
}
