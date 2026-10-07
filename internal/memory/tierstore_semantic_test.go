package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/scope"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// newTieredOverSQLite 构造"过程层落 tier 表 + 语义层复用 F-87 扁平表"的分层记忆。
func newTieredOverSQLite(t *testing.T, workingLimit int) (*TieredMemory, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "tiered-sem.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	flat := New(Options{Store: st, MaxPerScope: 50})
	tiered := NewTiered(TieredOptions{
		Store:        NewCompositeTierStore(NewSQLiteTierStore(st), NewSemanticTierStore(flat)),
		WorkingLimit: workingLimit,
		Consolidator: RuleConsolidator{MinScore: 0},
		Clock:        f49Clock(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Minute),
	})
	return tiered, st
}

// Test_F49_SemanticLayerReusesF87Dedup 是本轮接线的关键断言：
// 分层记忆的长期事实必须沿用 F-87 的判定（相似度分带），而不是只按文本指纹去重。
// 否则切换会让"同一件事被说两遍"在召回里出现两条。
//
// 直接走 st.UpsertSemantic 而不是 Save：**写入侧的覆盖判定现在发生在 Working 层**
// （见 Test_F49_SaveSupersedesSimilarWorkingItem），第二遍相似文本在 Save 时就被
// 吸收，固化因此不会触发。语义层的判定本身仍要单独钉住。
func Test_F49_SemanticLayerReusesF87Dedup(t *testing.T) {
	t.Parallel()
	tiered, _ := newTieredOverSQLite(t, 2)
	sc := ctxScope("g")

	for _, text := range []string{"用户喜欢喝橙汁", "用户喜欢喝橙汁！"} {
		if _, err := tiered.st.UpsertSemantic(sc, scope.ScopeFrom(sc), TierItem{Text: text}); err != nil {
			t.Fatalf("UpsertSemantic(%q): %v", text, err)
		}
	}

	facts, err := tiered.Semantics(sc)
	if err != nil {
		t.Fatalf("Semantics: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("F-87 判定应把高度相似的两条并入一条，实际 %d 条: %+v", len(facts), facts)
	}
}

// Test_F49_TieredMemoryAdminForgetAndList 覆盖 F-88 的两条工具路径在分层实现上仍成立：
// list_memories 只列长期事实；forget 与 forget_scope 能把三层一起清干净。
func Test_F49_TieredMemoryAdminForgetAndList(t *testing.T) {
	t.Parallel()
	// WorkingLimit 取 2：两次写入即触发一次固化（F-49 的固化触发条件是达到上限）。
	tiered, _ := newTieredOverSQLite(t, 50)
	scCtx := ctxScope("g")
	sc := scope.ScopeFrom(scCtx)

	// 直接写长期事实：走的是与固化完全相同的那条路径（语义层 = F-87 扁平表）。
	// 刻意用两条毫不相干的文本，避免测试依赖相似度阈值的具体取值。
	for _, text := range []string{"用户住在北京", "Python 的 GIL 限制了并行"} {
		if _, err := tiered.st.UpsertSemantic(scCtx, sc, TierItem{Text: text}); err != nil {
			t.Fatalf("UpsertSemantic: %v", err)
		}
	}
	// Working 里也放点东西：List 不应该把它列出来。
	if err := tiered.Save(scCtx, "最近说过的一句话"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	facts, err := tiered.List(scCtx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("List 应列出 2 条长期事实，实际 %d: %+v", len(facts), facts)
	}
	// 列表只含长期事实：Working 里也有内容，但不应出现在 List 中。
	working, err := tiered.Working(scCtx)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(working) == 0 {
		t.Fatal("Working 层应有内容（它是会话缓冲）")
	}

	if ok, ferr := tiered.Forget(scCtx, facts[0].ID); ferr != nil || !ok {
		t.Fatalf("Forget=(%v,%v)", ok, ferr)
	}
	left, err := tiered.List(scCtx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("删除一条后应剩 1 条，实际 %d", len(left))
	}

	n, err := tiered.ForgetScope(scCtx)
	if err != nil {
		t.Fatalf("ForgetScope: %v", err)
	}
	if n == 0 {
		t.Fatal("ForgetScope 应报告删除条数")
	}
	if left, _ := tiered.List(scCtx, 0); len(left) != 0 {
		t.Fatalf("清空后不应还有长期事实: %+v", left)
	}
	if w, _ := tiered.Working(scCtx); len(w) != 0 {
		t.Fatalf("清空后不应还有 Working: %+v", w)
	}
	if eps, _ := tiered.Episodes(scCtx, 0); len(eps) != 0 {
		t.Fatalf("清空后不应还有 Episodic: %+v", eps)
	}
}
