package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openPromptStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t, Options{Path: filepath.Join(t.TempDir(), "prompt.db")})
}

func Test_F89_RecordClassifiesRelation(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()

	first, err := s.RecordPromptSnapshot(ctx, "k", []string{"a", "b"}, "")
	if err != nil {
		t.Fatalf("RecordPromptSnapshot: %v", err)
	}
	if first.Relation != "first" || first.Seq != 1 {
		t.Fatalf("首条应标 first 且 seq=1: %+v", first)
	}

	ext, _ := s.RecordPromptSnapshot(ctx, "k", []string{"a", "b", "c"}, "")
	if ext.Relation != "extended" || ext.CommonPrefix != 2 || ext.Seq != 2 {
		t.Fatalf("应判为 extended: %+v", ext)
	}

	slid, _ := s.RecordPromptSnapshot(ctx, "k", []string{"b", "c", "d"}, "")
	if slid.Relation != "slid" || slid.SlidBy != 1 {
		t.Fatalf("应判为 slid: %+v", slid)
	}

	div, _ := s.RecordPromptSnapshot(ctx, "k", []string{"X", "c", "d"}, "")
	if div.Relation != "diverged" {
		t.Fatalf("应判为 diverged: %+v", div)
	}
	if n, _ := s.CountDivergedSnapshots(ctx); n != 1 {
		t.Fatalf("异常变化应可计数: %d", n)
	}
}

func Test_F89_IsScopedPerSession(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()
	_, _ = s.RecordPromptSnapshot(ctx, "a", []string{"x"}, "")
	b, _ := s.RecordPromptSnapshot(ctx, "b", []string{"x", "y"}, "")
	// 另一个会话的第一条不该被当成 a 的延续。
	if b.Relation != "first" || b.Seq != 1 {
		t.Fatalf("快照必须按会话隔离: %+v", b)
	}
}

func Test_F89_PruneIsRingBuffer(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := s.RecordPromptSnapshot(ctx, "k", []string{"m"}, ""); err != nil {
			t.Fatalf("RecordPromptSnapshot: %v", err)
		}
	}
	n, err := s.PrunePromptSnapshots(ctx, 3)
	if err != nil {
		t.Fatalf("PrunePromptSnapshots: %v", err)
	}
	if n != 7 {
		t.Fatalf("应裁掉 7 条: %d", n)
	}
	left, _ := s.ListPromptSnapshots(ctx, "k", 100)
	if len(left) != 3 {
		t.Fatalf("应保留 3 条: %d", len(left))
	}
	// 保留的必须是**最近**的 3 条。
	if left[0].Seq != 8 || left[2].Seq != 10 {
		t.Fatalf("应保留最近 3 条: %d..%d", left[0].Seq, left[2].Seq)
	}
}

func Test_F89_RejectsEmptySessionKey(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	if _, err := s.RecordPromptSnapshot(context.Background(), "  ", []string{"a"}, ""); err == nil {
		t.Fatalf("空会话键必须报错")
	}
}

// Test_F89_MemoryChangeIsExpectedNotDiverged 是真机实测暴露的误报的回归测试。
//
// 场景：写入一条记忆后，下一轮的记忆块变了。按 ADR-0002，记忆之后的内容全部失效，
// 因此 common_prefix 会退到 1（只剩 system 相同）。这是**预期**行为，
// 若报成 diverged，告警就会一直响，等于没有告警。
func Test_F89_MemoryChangeIsExpectedNotDiverged(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()

	// 第 1 轮：system + 记忆块 A + 历史 + 输入
	first := []string{"sys", "memA", "h1", "u1"}
	if _, err := s.RecordPromptSnapshot(ctx, "k", first, "memA"); err != nil {
		t.Fatalf("RecordPromptSnapshot: %v", err)
	}
	// 第 2 轮：记忆块变成 B，其余被顺延
	second := []string{"sys", "memB", "h1", "u1", "a1", "u2"}
	snap, err := s.RecordPromptSnapshot(ctx, "k", second, "memB")
	if err != nil {
		t.Fatalf("RecordPromptSnapshot: %v", err)
	}
	if snap.Relation != RelationMemoryChanged {
		t.Fatalf("记忆变更应判为 memory_changed（预期），实际 %q: %+v", snap.Relation, snap)
	}
	if snap.CommonPrefix != 1 {
		t.Fatalf("分歧点应在记忆块处: %+v", snap)
	}
	// 它不该被计入"异常变化"。
	if n, _ := s.CountDivergedSnapshots(ctx); n != 0 {
		t.Fatalf("记忆变更不得计入异常: %d", n)
	}
}

// Test_F89_MemoryUnchangedDivergenceIsStillReported 守住反例：
// 记忆没变却出现分歧，必须报出来。这才是这个特性真正的产出。
func Test_F89_MemoryUnchangedDivergenceIsStillReported(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()
	_, _ = s.RecordPromptSnapshot(ctx, "k", []string{"sys", "memA", "h1", "u1"}, "memA")

	// 记忆没变，但历史里的某条被改写了 —— 这才是意外。
	snap, _ := s.RecordPromptSnapshot(ctx, "k", []string{"sys", "memA", "X", "u1"}, "memA")
	if snap.Relation != "diverged" {
		t.Fatalf("记忆未变时仍应报 diverged: %+v", snap)
	}
	if !strings.Contains(snap.Relation, "diverged") {
		t.Fatalf("关系应为 diverged: %+v", snap)
	}
}

// Test_F89_MemoryChangeMasksSimultaneousChanges 如实记录该判据的**固有局限**。
//
// 记忆块位于第 1 位（ADR-0002），它一变，之后的一切都必然变。
// 因此当记忆块变化时，我们**无法**区分"只有记忆变了"与"记忆变了、别处也变了"——
// 两种情况都会表现为 common_prefix == 1。
//
// 这里把这个行为固定下来，而不是假装能分辨：真正的意外变化若恰好与记忆
// 变更发生在同一轮，会被归到 memory_changed（INFO 级），而不是 diverged（WARN 级）。
func Test_F89_MemoryChangeMasksSimultaneousChanges(t *testing.T) {
	t.Parallel()
	s := openPromptStore(t)
	ctx := context.Background()
	_, _ = s.RecordPromptSnapshot(ctx, "k", []string{"sys", "memA", "h1", "u1"}, "memA")

	snap, _ := s.RecordPromptSnapshot(ctx, "k", []string{"sys", "memB", "X", "u1"}, "memB")
	if snap.Relation != RelationMemoryChanged {
		t.Fatalf("记忆变更会掩盖同轮的其他变化，这是已知局限: %+v", snap)
	}
	// 但**没有**记忆变更时，同样的历史改写会被正确报为异常——
	// 也就是说这条局限只在"记忆恰好同轮变化"时生效，不影响绝大多数情况。
	s2 := openPromptStore(t)
	_, _ = s2.RecordPromptSnapshot(ctx, "k2", []string{"sys", "memA", "h1", "u1"}, "memA")
	snap2, _ := s2.RecordPromptSnapshot(ctx, "k2", []string{"sys", "memA", "X", "u1"}, "memA")
	if snap2.Relation != "diverged" {
		t.Fatalf("无记忆变更时应正常报异常: %+v", snap2)
	}
}
