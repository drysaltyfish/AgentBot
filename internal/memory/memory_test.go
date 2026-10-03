package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// fakeJudge 是确定性判官，避免单测依赖真实模型。
type fakeJudge struct {
	same  bool
	err   error
	calls int
	mu    sync.Mutex
}

func (f *fakeJudge) SameFact(ctx context.Context, a, b string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.same, f.err
}

func newStore(t *testing.T, judge Judge) *Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "mem.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(Options{Store: st, Judge: judge})
}

func ctxScope(scope string) context.Context {
	return tool.WithScope(context.Background(), scope)
}

func recall(t *testing.T, s *Store, scope string) []string {
	t.Helper()
	got, err := s.Recall(ctxScope(scope))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	return got
}

func Test_F87_SavesIntoScope(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	if err := s.Save(ctxScope("g1"), "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := recall(t, s, "g1"); len(got) != 1 || got[0] != "我喜欢喝橙汁" {
		t.Fatalf("应召回一条: %v", got)
	}
	if got := recall(t, s, "g2"); len(got) != 0 {
		t.Fatalf("不得跨作用域: %v", got)
	}
}

func Test_F87_RequiresScope(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	if err := s.Save(context.Background(), "x"); err == nil {
		t.Fatalf("无作用域必须报错")
	}
}

func Test_F87_ValidatesText(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	ctx := ctxScope("g")
	if err := s.Save(ctx, "   "); !errors.Is(err, ErrEmpty) {
		t.Fatalf("空内容应报 ErrEmpty: %v", err)
	}
	if err := s.Save(ctx, "a"+"\n"+"b"); !errors.Is(err, ErrMultiline) {
		t.Fatalf("多行应报 ErrMultiline: %v", err)
	}
	if err := s.Save(ctx, strings.Repeat("字", Limit+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("超长应报 ErrTooLong: %v", err)
	}
}

// Test_F87_ExactDuplicateIsIgnoredWithoutAskingJudge 守住确定性快路径。
func Test_F87_ExactDuplicateIsIgnoredWithoutAskingJudge(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{same: true}
	s := newStore(t, j)
	ctx := ctxScope("g")
	if err := s.Save(ctx, "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(ctx, "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if j.calls != 0 {
		t.Fatalf("完全相同不该问判官: %d 次", j.calls)
	}
	if got := recall(t, s, "g"); len(got) != 1 {
		t.Fatalf("应仍是一条: %v", got)
	}
}

// Test_F87_AmbiguousBandAsksJudgeAndKeepsDistinctFacts 是实测失败的那一对。
func Test_F87_AmbiguousBandAsksJudgeAndKeepsDistinctFacts(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{same: false}
	s := newStore(t, j)
	ctx := ctxScope("g")
	if err := s.Save(ctx, "旧的一条"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(ctx, "新的一条"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if j.calls != 1 {
		t.Fatalf("歧义带应问一次判官: %d", j.calls)
	}
	if got := recall(t, s, "g"); len(got) != 2 {
		t.Fatalf("两件不同的事都应保留: %v", got)
	}
}

// Test_F87_AmbiguousBandMergesParaphrase 是原本要修的那一对。
func Test_F87_AmbiguousBandMergesParaphrase(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{same: true}
	s := newStore(t, j)
	ctx := ctxScope("g")
	if err := s.Save(ctx, "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(ctx, "用户喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if j.calls != 1 {
		t.Fatalf("歧义带应问一次判官: %d", j.calls)
	}
	got := recall(t, s, "g")
	if len(got) != 1 {
		t.Fatalf("同一件事应合并为一条: %v", got)
	}
	if got[0] != "用户喜欢喝橙汁" {
		t.Fatalf("就地更新应采用新文本: %v", got)
	}
}

// Test_F87_JudgeFailureFallsBackToThreshold 覆盖用户选定的失败语义。
func Test_F87_JudgeFailureFallsBackToThreshold(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{err: errors.New("judge down")}
	s := newStore(t, j)
	ctx := ctxScope("g")
	if err := s.Save(ctx, "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(ctx, "用户喜欢喝橙汁"); err != nil {
		t.Fatalf("判官失败不得让写入失败: %v", err)
	}
	if got := recall(t, s, "g"); len(got) != 1 {
		t.Fatalf("应按确定性阈值并入: %v", got)
	}
	if s.JudgeFallbacks() != 1 {
		t.Fatalf("应记录一次回退: %d", s.JudgeFallbacks())
	}
}

func Test_F87_NoJudgeUsesThreshold(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	ctx := ctxScope("g")
	_ = s.Save(ctx, "我喜欢喝橙汁")
	_ = s.Save(ctx, "用户喜欢喝橙汁")
	_ = s.Save(ctx, "我喜欢喝冰美式")
	got := recall(t, s, "g")
	if len(got) != 2 {
		t.Fatalf("无判官时应按阈值判定: %v", got)
	}
}

func Test_F87_VerdictCacheAvoidsRepeatCalls(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{same: false}
	s := newStore(t, j)
	ctx := ctxScope("g")
	_ = s.Save(ctx, "旧的一条")
	for i := 0; i < 3; i++ {
		if err := s.Save(ctx, "新的一条"); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if j.calls != 1 {
		t.Fatalf("同一对文本只该问一次（判答缓存）: %d", j.calls)
	}
}

func Test_LLMJudge_ParseVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
		ok   bool
	}{
		{"是", true, true},
		{"是的", true, true},
		{" 否 ", false, true},
		{"不一样", false, true},
		{"不是同一件事", false, true},
		{"yes", true, true},
		{"no", false, true},
		{"", false, false},
		{"大概吧", false, false},
	}
	for _, tc := range cases {
		got, err := ParseVerdict(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Fatalf("ParseVerdict(%q) = (%v, %v)，期望 (%v, ok=%v)", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func Test_LLMJudge_CachesByUnorderedPair(t *testing.T) {
	t.Parallel()
	j := &fakeJudge{same: true}
	s := newStore(t, j)
	ctx := ctxScope("g")
	_ = s.Save(ctx, "我喜欢喝橙汁")
	_ = s.Save(ctx, "用户喜欢喝橙汁")
	before := j.calls
	_ = s.Save(ctx, "用户喜欢喝橙汁")
	if j.calls != before {
		t.Fatalf("重复写入不该再问判官: %d -> %d", before, j.calls)
	}
}
