package scope

import (
	"context"
	"testing"
)

// TestRoundTrip 锁住基本语义：放进去能取出来。
func TestRoundTrip(t *testing.T) {
	ctx := WithScope(context.Background(), "group-1")
	if got := ScopeFrom(ctx); got != "group-1" {
		t.Fatalf("ScopeFrom = %q, want %q", got, "group-1")
	}
}

// TestAbsentIsEmpty 锁住缺省语义：没放过时返回空串，而不是 panic 或 "default"。
func TestAbsentIsEmpty(t *testing.T) {
	if got := ScopeFrom(context.Background()); got != "" {
		t.Fatalf("ScopeFrom(empty) = %q, want empty", got)
	}
	// 刻意传 nil：ScopeFrom 的契约就是"nil ctx 也安全"。
	// staticcheck 的 SA1012 防的是"该用 context.TODO() 却传了 nil"的误用，
	// 而这里正是要断言 nil 的容错，因此显式豁免。
	var nilCtx context.Context
	if got := ScopeFrom(nilCtx); got != "" { //nolint:staticcheck // 见上：故意断言 nil ctx 的容错
		t.Fatalf("ScopeFrom(nil) = %q, want empty", got)
	}
}

// TestNestedOverrides 锁住嵌套覆盖：后放的作用域覆盖先放的。
func TestNestedOverrides(t *testing.T) {
	ctx := WithScope(context.Background(), "a")
	ctx = WithScope(ctx, "b")
	if got := ScopeFrom(ctx); got != "b" {
		t.Fatalf("ScopeFrom(nested) = %q, want %q", got, "b")
	}
}
