package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/store"
)

// openTierStore 打开一个临时 SQLite 库。
func openTierStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return st
}

// Test_F49_SQLiteTierStoreWorkingLifecycle 覆盖 Working 的追加/裁剪/删除语义，
// 与 MemTierStore 保持一致（裁剪返回被移除的旧条目，供固化使用）。
func Test_F49_SQLiteTierStoreWorkingLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTierStore(t, filepath.Join(t.TempDir(), "tier.db"))
	defer func() { _ = st.Close() }()
	ts := NewSQLiteTierStore(st)

	var ids []int64
	for i := 0; i < 3; i++ {
		it, err := ts.AppendWorking(ctx, "g", TierItem{Text: fmt.Sprintf("w-%d", i)})
		if err != nil {
			t.Fatalf("AppendWorking: %v", err)
		}
		if it.ID == 0 || it.Tier != TierWorking {
			t.Fatalf("追加应返回带 ID 的 working 条目: %+v", it)
		}
		ids = append(ids, it.ID)
	}
	if ids[0] >= ids[1] || ids[1] >= ids[2] {
		t.Fatalf("ID 必须单调递增: %v", ids)
	}

	all, err := ts.Working(ctx, "g")
	if err != nil || len(all) != 3 {
		t.Fatalf("Working=(%d,%v), want 3 条", len(all), err)
	}
	if all[0].Text != "w-0" || all[2].Text != "w-2" {
		t.Fatalf("Working 必须按创建顺序: %+v", all)
	}

	removed, err := ts.TrimWorking(ctx, "g", 1)
	if err != nil {
		t.Fatalf("TrimWorking: %v", err)
	}
	if len(removed) != 2 || removed[0].Text != "w-0" || removed[1].Text != "w-1" {
		t.Fatalf("裁剪应返回最旧的两条: %+v", removed)
	}
	kept, _ := ts.Working(ctx, "g")
	if len(kept) != 1 || kept[0].Text != "w-2" {
		t.Fatalf("裁剪后应只剩最新一条: %+v", kept)
	}

	ok, err := ts.DeleteWorking(ctx, "g", kept[0].ID)
	if err != nil || !ok {
		t.Fatalf("DeleteWorking=(%v,%v)", ok, err)
	}
	if again, _ := ts.DeleteWorking(ctx, "g", kept[0].ID); again {
		t.Fatal("重复删除应返回 false")
	}
	if left, _ := ts.Working(ctx, "g"); len(left) != 0 {
		t.Fatalf("删除后应为空: %+v", left)
	}
}

// Test_F49_SQLiteTierStoreEpisodesAndItems 覆盖片段聚合、条目删除与片段清理。
func Test_F49_SQLiteTierStoreEpisodesAndItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTierStore(t, filepath.Join(t.TempDir(), "tier.db"))
	defer func() { _ = st.Close() }()
	ts := NewSQLiteTierStore(st)

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ep1, err := ts.AppendEpisode(ctx, "g", Episode{
		StartedAt: base, EndedAt: base.Add(time.Minute),
		Items: []TierItem{{Text: "e1-a"}, {Text: "e1-b"}},
	})
	if err != nil {
		t.Fatalf("AppendEpisode: %v", err)
	}
	if ep1.ID == 0 || len(ep1.Items) != 2 || ep1.Items[0].Tier != TierEpisodic {
		t.Fatalf("片段应带 ID 与层级: %+v", ep1)
	}
	if _, err := ts.AppendEpisode(ctx, "g", Episode{
		StartedAt: base.Add(time.Hour), EndedAt: base.Add(time.Hour),
		Items: []TierItem{{Text: "e2-a"}},
	}); err != nil {
		t.Fatalf("AppendEpisode 2: %v", err)
	}

	eps, err := ts.Episodes(ctx, "g", 0)
	if err != nil || len(eps) != 2 {
		t.Fatalf("Episodes=(%d,%v), want 2", len(eps), err)
	}
	if len(eps[0].Items) != 2 || eps[1].Items[0].Text != "e2-a" {
		t.Fatalf("片段条目未按序保留: %+v", eps)
	}
	if last, _ := ts.Episodes(ctx, "g", 1); len(last) != 1 || last[0].ID != eps[1].ID {
		t.Fatalf("limit 应取最新片段: %+v", last)
	}

	// 删除片段里的一个条目：片段保留；删空后片段一并删除。
	ok, err := ts.DeleteEpisodeItem(ctx, "g", ep1.Items[0].ID)
	if err != nil || !ok {
		t.Fatalf("DeleteEpisodeItem=(%v,%v)", ok, err)
	}
	eps, _ = ts.Episodes(ctx, "g", 0)
	if len(eps) != 2 || len(eps[0].Items) != 1 {
		t.Fatalf("删一条后片段应保留: %+v", eps)
	}
	if ok, _ := ts.DeleteEpisodeItem(ctx, "g", ep1.Items[1].ID); !ok {
		t.Fatal("删除最后一个条目应成功")
	}
	eps, _ = ts.Episodes(ctx, "g", 0)
	if len(eps) != 1 {
		t.Fatalf("条目清空后片段应被删除: %+v", eps)
	}

	if n, err := ts.TrimEpisodes(ctx, "g", 0); err != nil || n != 1 {
		t.Fatalf("TrimEpisodes=(%d,%v), want 1", n, err)
	}
	if eps, _ = ts.Episodes(ctx, "g", 0); len(eps) != 0 {
		t.Fatalf("全部裁剪后应为空: %+v", eps)
	}
}

// Test_F49_SQLiteTierStoreSemanticUpsertAndTrim 覆盖长期事实的去重更新与淘汰顺序。
func Test_F49_SQLiteTierStoreSemanticUpsertAndTrim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTierStore(t, filepath.Join(t.TempDir(), "tier.db"))
	defer func() { _ = st.Close() }()
	ts := NewSQLiteTierStore(st)

	added, err := ts.UpsertSemantic(ctx, "g", TierItem{Text: "用户爱喝橙汁", Title: "偏好"})
	if err != nil || !added {
		t.Fatalf("首次写入应返回 added=true: (%v,%v)", added, err)
	}
	added, err = ts.UpsertSemantic(ctx, "g", TierItem{Text: "用户爱喝橙汁", Title: "口味偏好", Score: 0.9})
	if err != nil || added {
		t.Fatalf("同文本重复写入应就地更新: (%v,%v)", added, err)
	}
	facts, _ := ts.Semantics(ctx, "g")
	if len(facts) != 1 || facts[0].Title != "口味偏好" || facts[0].Score != 0.9 {
		t.Fatalf("更新未生效或产生重复: %+v", facts)
	}
	if facts[0].Tier != TierSemantic {
		t.Fatalf("长期事实层级应为 semantic: %+v", facts[0])
	}

	if _, err := ts.UpsertSemantic(ctx, "g", TierItem{Text: "用户在北京", Score: 0.1}); err != nil {
		t.Fatalf("UpsertSemantic: %v", err)
	}
	if _, err := ts.UpsertSemantic(ctx, "g", TierItem{Text: "用户养了一只猫", Score: 0.5}); err != nil {
		t.Fatalf("UpsertSemantic: %v", err)
	}
	dropped, err := ts.TrimSemantics(ctx, "g", 1)
	if err != nil || dropped != 2 {
		t.Fatalf("TrimSemantics=(%d,%v), want 2", dropped, err)
	}
	facts, _ = ts.Semantics(ctx, "g")
	if len(facts) != 1 || facts[0].Text != "用户爱喝橙汁" {
		t.Fatalf("应保留高分条目: %+v", facts)
	}
	if ok, err := ts.DeleteSemantic(ctx, "g", facts[0].ID); err != nil || !ok {
		t.Fatalf("DeleteSemantic=(%v,%v)", ok, err)
	}
}

// Test_F49_SQLiteTierStoreScopesAreIsolated 钉住 F-47：作用域必须参与每条路径。
func Test_F49_SQLiteTierStoreScopesAreIsolated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTierStore(t, filepath.Join(t.TempDir(), "tier.db"))
	defer func() { _ = st.Close() }()
	ts := NewSQLiteTierStore(st)

	if _, err := ts.AppendWorking(ctx, "a", TierItem{Text: "a-1"}); err != nil {
		t.Fatalf("AppendWorking: %v", err)
	}
	if _, err := ts.UpsertSemantic(ctx, "b", TierItem{Text: "b-1"}); err != nil {
		t.Fatalf("UpsertSemantic: %v", err)
	}
	if got, _ := ts.Working(ctx, "b"); len(got) != 0 {
		t.Fatalf("作用域 b 不应看到 a 的 working: %+v", got)
	}
	if got, _ := ts.Semantics(ctx, "a"); len(got) != 0 {
		t.Fatalf("作用域 a 不应看到 b 的 semantic: %+v", got)
	}
}

// Test_F49_SQLiteTierStoreSurvivesReopen 是本实现存在的理由：
// 内存实现重启即丢记忆，而 SQLite 实现必须把三层与 ID 原样带回来。
func Test_F49_SQLiteTierStoreSurvivesReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persist.db")

	first := openTierStore(t, path)
	ts := NewSQLiteTierStore(first)
	working, err := ts.AppendWorking(ctx, "g", TierItem{Text: "重启前的短期记忆"})
	if err != nil {
		t.Fatalf("AppendWorking: %v", err)
	}
	ep, err := ts.AppendEpisode(ctx, "g", Episode{Items: []TierItem{{Text: "重启前的情节"}}})
	if err != nil {
		t.Fatalf("AppendEpisode: %v", err)
	}
	if _, err := ts.UpsertSemantic(ctx, "g", TierItem{Text: "重启前的事实"}); err != nil {
		t.Fatalf("UpsertSemantic: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := openTierStore(t, path)
	defer func() { _ = second.Close() }()
	ts2 := NewSQLiteTierStore(second)
	w2, _ := ts2.Working(ctx, "g")
	if len(w2) != 1 || w2[0].ID != working.ID || w2[0].Text != working.Text {
		t.Fatalf("Working 未跨重启保留: %+v (want ID=%d)", w2, working.ID)
	}
	e2, _ := ts2.Episodes(ctx, "g", 0)
	if len(e2) != 1 || e2[0].ID != ep.ID || len(e2[0].Items) != 1 {
		t.Fatalf("Episodic 未跨重启保留: %+v", e2)
	}
	f2, _ := ts2.Semantics(ctx, "g")
	if len(f2) != 1 || f2[0].Text != "重启前的事实" {
		t.Fatalf("Semantic 未跨重启保留: %+v", f2)
	}
	next, err := ts2.AppendWorking(ctx, "g", TierItem{Text: "重启后的新记忆"})
	if err != nil {
		t.Fatalf("AppendWorking after reopen: %v", err)
	}
	if next.ID <= working.ID {
		t.Fatalf("重启后 ID 回退，会与旧条目相撞: new=%d old=%d", next.ID, working.ID)
	}
}

// Test_F49_TieredMemoryOverSQLiteEndToEnd 是 F-49 的核心验收跑在真实持久层上：
// 100 条消息后 Working 被裁剪、Episodic 有片段、Semantic 有固化条目，且重启后仍在。
func Test_F49_TieredMemoryOverSQLiteEndToEnd(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tiered.db")
	st := openTierStore(t, path)

	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tm := NewTiered(TieredOptions{
		Store:        NewSQLiteTierStore(st),
		WorkingLimit: 5,
		Clock:        f49Clock(start, time.Minute),
	})
	scopeCtx := ctxScope("g")
	for i := 0; i < 100; i++ {
		if err := tm.Save(scopeCtx, fmt.Sprintf("消息-%d", i)); err != nil {
			t.Fatalf("Save(%d): %v", i, err)
		}
	}
	if err := tm.WaitConsolidation(context.Background()); err != nil {
		t.Fatalf("WaitConsolidation: %v", err)
	}
	working, err := tm.Working(scopeCtx)
	if err != nil || len(working) == 0 || len(working) > 5 {
		t.Fatalf("Working 应有界且非空: %d (%v)", len(working), err)
	}
	eps, err := tm.Episodes(scopeCtx, 0)
	if err != nil || len(eps) == 0 {
		t.Fatalf("Episodic 应生成片段: %d (%v)", len(eps), err)
	}
	facts, err := tm.Semantics(scopeCtx)
	if err != nil || len(facts) == 0 {
		t.Fatalf("Semantic 应有固化条目: %d (%v)", len(facts), err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st2 := openTierStore(t, path)
	defer func() { _ = st2.Close() }()
	tm2 := NewTiered(TieredOptions{Store: NewSQLiteTierStore(st2), WorkingLimit: 5, Clock: f49Clock(start, time.Minute)})
	if got, _ := tm2.Working(scopeCtx); len(got) != len(working) {
		t.Fatalf("重启后 Working 丢失: %d -> %d", len(working), len(got))
	}
	if got, _ := tm2.Semantics(scopeCtx); len(got) != len(facts) {
		t.Fatalf("重启后 Semantic 丢失: %d -> %d", len(facts), len(got))
	}
}
