package store

import (
	"context"
	"path/filepath"
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

	first, err := s.RecordPromptSnapshot(ctx, "k", []string{"a", "b"})
	if err != nil {
		t.Fatalf("RecordPromptSnapshot: %v", err)
	}
	if first.Relation != "first" || first.Seq != 1 {
		t.Fatalf("首条应标 first 且 seq=1: %+v", first)
	}

	ext, _ := s.RecordPromptSnapshot(ctx, "k", []string{"a", "b", "c"})
	if ext.Relation != "extended" || ext.CommonPrefix != 2 || ext.Seq != 2 {
		t.Fatalf("应判为 extended: %+v", ext)
	}

	slid, _ := s.RecordPromptSnapshot(ctx, "k", []string{"b", "c", "d"})
	if slid.Relation != "slid" || slid.SlidBy != 1 {
		t.Fatalf("应判为 slid: %+v", slid)
	}

	div, _ := s.RecordPromptSnapshot(ctx, "k", []string{"X", "c", "d"})
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
	_, _ = s.RecordPromptSnapshot(ctx, "a", []string{"x"})
	b, _ := s.RecordPromptSnapshot(ctx, "b", []string{"x", "y"})
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
		if _, err := s.RecordPromptSnapshot(ctx, "k", []string{"m"}); err != nil {
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
	if _, err := s.RecordPromptSnapshot(context.Background(), "  ", []string{"a"}); err == nil {
		t.Fatalf("空会话键必须报错")
	}
}
