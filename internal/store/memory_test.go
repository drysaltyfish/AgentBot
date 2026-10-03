package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openMemStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t, Options{Path: filepath.Join(t.TempDir(), "mem.db")})
}

func saveMem(t *testing.T, s *Store, scope, text string) MemoryWriteResult {
	t.Helper()
	res, err := s.SaveMemory(context.Background(), Memory{ScopeKey: scope, Text: text})
	if err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	return res
}

func Test_F87_AddsNewMemory(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	res := saveMem(t, s, "g1", "我喜欢喝橙汁")
	if res.Decision != MemoryAdded {
		t.Fatalf("首次写入应为 added: %+v", res)
	}
	if res.Reason == "" {
		t.Fatalf("必须给出判定理由: %+v", res)
	}
	got, err := s.RecallMemories(context.Background(), "g1")
	if err != nil {
		t.Fatalf("RecallMemories: %v", err)
	}
	if len(got) != 1 || got[0].Text != "我喜欢喝橙汁" {
		t.Fatalf("应召回一条: %+v", got)
	}
}

// Test_F87_IdenticalTextIsIgnored 覆盖幂等：同样的写入重放不产生新条目。
func Test_F87_IdenticalTextIsIgnored(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	_ = saveMem(t, s, "g1", "我喜欢喝橙汁")
	res := saveMem(t, s, "g1", "我喜欢喝橙汁")
	if res.Decision != MemoryIgnored {
		t.Fatalf("完全相同的写入应被忽略: %+v", res)
	}
	if n, _ := s.CountMemories(context.Background(), "g1"); n != 1 {
		t.Fatalf("不应产生新条目: %d", n)
	}
}

// Test_F87_ParaphraseUpdatesInPlace 是实测出现过的那对文本。
//
// 事故现场：规则触发写入「我喜欢喝橙汁」，模型随后写入「用户喜欢喝橙汁」，存了两份。
// 现在应当是**就地更新**：id 与顺序都不变。
func Test_F87_ParaphraseUpdatesInPlace(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	first := saveMem(t, s, "g1", "我喜欢喝橙汁")
	// 中间插一条无关的，用来验证顺序。
	_ = saveMem(t, s, "g1", "讨厌香菜")
	_ = saveMem(t, s, "g1", "住在杭州")

	res := saveMem(t, s, "g1", "用户喜欢喝橙汁")
	if res.Decision != MemoryUpdated {
		t.Fatalf("近似改写应就地更新: %+v", res)
	}
	if res.ID != first.ID {
		t.Fatalf("更新必须保持原 id（否则顺序会位移）: %d vs %d", res.ID, first.ID)
	}
	if res.Similarity < 0.5 {
		t.Fatalf("理由里应给出相似度: %+v", res)
	}
	if !strings.Contains(res.Reason, "并入") {
		t.Fatalf("理由应可解释: %q", res.Reason)
	}

	got, _ := s.RecallMemories(context.Background(), "g1")
	if len(got) != 3 {
		t.Fatalf("仍应是 3 条: %+v", got)
	}
	// 顺序不变，第一条被就地改写。
	if got[0].ID != first.ID || got[0].Text != "用户喜欢喝橙汁" {
		t.Fatalf("第一条应被就地更新: %+v", got[0])
	}
	if got[1].Text != "讨厌香菜" || got[2].Text != "住在杭州" {
		t.Fatalf("其它条目的顺序不得改变: %+v", got)
	}
}

// Test_F87_DistinctFactsAreBothKept 守住"不该合并"的反例。
func Test_F87_DistinctFactsAreBothKept(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	_ = saveMem(t, s, "g1", "我喜欢喝橙汁")
	res := saveMem(t, s, "g1", "我喜欢喝冰美式")
	if res.Decision != MemoryAdded {
		t.Fatalf("不同的事实都应保留: %+v", res)
	}
	got, _ := s.RecallMemories(context.Background(), "g1")
	if len(got) != 2 {
		t.Fatalf("应有 2 条: %+v", got)
	}
}

func Test_F87_ScopeIsolation(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	_ = saveMem(t, s, "g1", "群 A 的秘密")
	_ = saveMem(t, s, "g2", "群 B 的事")

	// 相同内容在不同作用域是**两条**：隔离优先于去重。
	_ = saveMem(t, s, "g2", "群 A 的秘密")

	gotA, _ := s.RecallMemories(context.Background(), "g1")
	gotB, _ := s.RecallMemories(context.Background(), "g2")
	if len(gotA) != 1 || len(gotB) != 2 {
		t.Fatalf("作用域未隔离: A=%d B=%d", len(gotA), len(gotB))
	}
	for _, m := range gotB {
		if m.ScopeKey != "g2" {
			t.Fatalf("召回串了作用域: %+v", m)
		}
	}
}

func Test_F87_RejectsBadInput(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	ctx := context.Background()
	if _, err := s.SaveMemory(ctx, Memory{ScopeKey: "  ", Text: "x"}); err == nil {
		t.Fatalf("空作用域必须报错")
	}
	if _, err := s.SaveMemory(ctx, Memory{ScopeKey: "g", Text: "   "}); err == nil {
		t.Fatalf("空内容必须报错")
	}
}

// Test_F88_ForgetIsIdempotent 覆盖 F-88 的删除幂等。
func Test_F88_ForgetIsIdempotent(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	ctx := context.Background()
	res := saveMem(t, s, "g1", "要忘掉的事")

	deleted, err := s.ForgetMemory(ctx, "g1", res.ID)
	if err != nil || !deleted {
		t.Fatalf("首次删除应成功: deleted=%v err=%v", deleted, err)
	}
	// 再删一次：不算错误。
	deleted, err = s.ForgetMemory(ctx, "g1", res.ID)
	if err != nil {
		t.Fatalf("重复删除不应报错: %v", err)
	}
	if deleted {
		t.Fatalf("重复删除应返回 false")
	}
	got, _ := s.RecallMemories(ctx, "g1")
	if len(got) != 0 {
		t.Fatalf("删除后不应召回: %+v", got)
	}
	// 跨作用域删不掉别人的。
	keep := saveMem(t, s, "g2", "别的群的事")
	if deleted, _ := s.ForgetMemory(ctx, "g1", keep.ID); deleted {
		t.Fatalf("不得跨作用域删除")
	}
}

// Test_F88_TrimByScoreAndLru 覆盖留存淘汰。
func Test_F88_TrimByScoreAndLru(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	ctx := context.Background()
	ids := make([]int64, 0, 5)
	for i, text := range []string{"第一条", "第二条", "第三条", "第四条", "第五条"} {
		res, err := s.SaveMemory(ctx, Memory{ScopeKey: "g", Text: text, Score: float64(i)})
		if err != nil {
			t.Fatalf("SaveMemory: %v", err)
		}
		ids = append(ids, res.ID)
	}
	// 保留 2 条：分值最低的三条先走。
	removed, err := s.TrimMemories(ctx, "g", 2)
	if err != nil {
		t.Fatalf("TrimMemories: %v", err)
	}
	if removed != 3 {
		t.Fatalf("应淘汰 3 条，实际 %d", removed)
	}
	got, _ := s.RecallMemories(ctx, "g")
	if len(got) != 2 {
		t.Fatalf("应剩 2 条: %+v", got)
	}
	// 剩下的是分值最高的两条（第四条 score=3、第五条 score=4），且保持原顺序。
	if got[0].Text != "第四条" || got[1].Text != "第五条" {
		t.Fatalf("应保留分值最高的两条: %+v", got)
	}
	_ = ids
}

func Test_F88_ForgetScopeClearsOnlyThatScope(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	ctx := context.Background()
	_ = saveMem(t, s, "a", "a1")
	_ = saveMem(t, s, "a", "a2")
	_ = saveMem(t, s, "b", "b1")

	n, err := s.ForgetScope(ctx, "a")
	if err != nil || n != 2 {
		t.Fatalf("应删除 2 条: n=%d err=%v", n, err)
	}
	if c, _ := s.CountMemories(ctx, "b"); c != 1 {
		t.Fatalf("不得影响其它作用域: %d", c)
	}
}

func Test_F88_ListMemoriesIsNewestFirst(t *testing.T) {
	t.Parallel()
	s := openMemStore(t)
	ctx := context.Background()
	_ = saveMem(t, s, "g", "旧的一条")
	_ = saveMem(t, s, "g", "新的一条")
	got, err := s.ListMemories(ctx, "g", 10)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 条: %+v", got)
	}
	// 两条的 updated_at 可能相同毫秒，此时按 id 倒序，仍是新的在前。
	if got[0].UpdatedAt < got[1].UpdatedAt {
		t.Fatalf("应按更新时间倒序: %+v", got)
	}
}
