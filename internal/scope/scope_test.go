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
	if got := ScopeFrom(nil); got != "" {
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
