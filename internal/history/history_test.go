package history

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func call(id string) Item {
	return Item{Kind: KindAssistant, Content: "calling " + id, ToolCalls: []ToolCall{{ID: id, Name: "tool", Arguments: "{}"}}}
}

func result(id string) Item {
	return Item{Kind: KindToolResult, ToolCallID: id, Content: "result " + id}
}

func Test_F38_EmptyHistoryReturnsEmptySliceNotNil(t *testing.T) {
	t.Parallel()
	m := NewMemory(10)
	got, err := m.Messages(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if got == nil {
		t.Fatalf("Messages: actual=nil expected=empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("Messages: actual=%d expected=0", len(got))
	}
}

func Test_F38_MessagesReturnsCopy(t *testing.T) {
	t.Parallel()
	m := NewMemory(10)
	ctx := context.Background()
	orig := call("c1")
	if err := m.Append(ctx, "k", orig); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := m.Messages(ctx, "k")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	got[0].Content = "mutated"
	got[0].ToolCalls[0].Name = "mutated"
	again, _ := m.Messages(ctx, "k")
	if again[0].Content != "calling c1" || again[0].ToolCalls[0].Name != "tool" {
		t.Fatalf("internal state mutated through returned slice: %+v", again[0])
	}
}

func Test_F38_WindowKeepsToolCallPairIntact(t *testing.T) {
	t.Parallel()
	items := []Item{
		{Kind: KindUser, Content: "hi"},
		call("c1"),
		result("c1"),
	}
	for _, n := range []int{1, 2, 3} {
		got := Window{N: n}.Apply(items)
		if Orphans(got) != 0 {
			t.Fatalf("N=%d produced %d orphan tool results: %+v", n, Orphans(got), got)
		}
		if DanglingToolCalls(got) != 0 {
			t.Fatalf("N=%d produced %d dangling tool_calls: %+v", n, DanglingToolCalls(got), got)
		}
		for _, it := range got {
			if it.Kind == KindToolResult && it.ToolCallID == "c1" {
				found := false
				for _, other := range got {
					if other.Kind == KindAssistant {
						for _, tc := range other.ToolCalls {
							if tc.ID == "c1" {
								found = true
							}
						}
					}
				}
				if !found {
					t.Fatalf("N=%d split the tool call from its result: %+v", n, got)
				}
			}
		}
	}

	// N=1 时宁可超出预算，也要保住配对。
	got := Window{N: 1}.Apply(items)
	if len(got) != 2 || got[0].Kind != KindAssistant || got[1].Kind != KindToolResult {
		t.Fatalf("N=1 window: actual=%+v expected=[assistant toolResult]", got)
	}
}

func Test_F38_TrimDropsUnrecoverableOrphan(t *testing.T) {
	t.Parallel()
	items := []Item{
		{Kind: KindUser, Content: "hi"},
		{Kind: KindToolResult, ToolCallID: "ghost", Content: "orphan"},
	}
	got := Window{N: 1}.Apply(items)
	if len(got) != 0 {
		t.Fatalf("unrecoverable orphan should be dropped: actual=%+v", got)
	}
	if Orphans(got) != 0 {
		t.Fatalf("orphans remain: %d", Orphans(got))
	}
}

func Test_F38_AppendEnforcesMaxAndKeepsPairs(t *testing.T) {
	t.Parallel()
	m := NewMemory(3)
	ctx := context.Background()
	for _, it := range []Item{
		{Kind: KindUser, Content: "u1"},
		call("c1"),
		result("c1"),
		{Kind: KindUser, Content: "u2"},
		{Kind: KindUser, Content: "u3"},
	} {
		if err := m.Append(ctx, "k", it); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, _ := m.Messages(ctx, "k")
	if Orphans(got) != 0 {
		t.Fatalf("Append trimming produced orphans: %+v", got)
	}
	if DanglingToolCalls(got) != 0 {
		t.Fatalf("Append trimming produced dangling tool_calls: %+v", got)
	}
	if len(got) > 4 {
		t.Fatalf("max not enforced: actual=%d expected<=4", len(got))
	}
}

func Test_F38_TrimAndReset(t *testing.T) {
	t.Parallel()
	m := NewMemory(100)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := m.Append(ctx, "k", Item{Kind: KindUser, Content: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := m.Trim(ctx, "k", 4); err != nil {
		t.Fatalf("Trim: %v", err)
	}
	got, _ := m.Messages(ctx, "k")
	if len(got) != 4 || got[3].Content != "m9" {
		t.Fatalf("after Trim: actual=%d last=%q expected=4/m9", len(got), got[len(got)-1].Content)
	}
	if err := m.Reset(ctx, "k"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if m.Len("k") != 0 {
		t.Fatalf("after Reset: actual=%d expected=0", m.Len("k"))
	}
}

func Test_F38_ConcurrentAppendAndMessages(t *testing.T) {
	t.Parallel()
	m := NewMemory(5000)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 125; i++ {
				if err := m.Append(ctx, "k", Item{Kind: KindUser, Content: fmt.Sprintf("g%d-%d", g, i)}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			items, err := m.Messages(ctx, "k")
			if err != nil {
				t.Errorf("Messages: %v", err)
				return
			}
			if Orphans(items) != 0 {
				t.Errorf("orphans in concurrent read")
				return
			}
		}
	}()
	wg.Wait()
	if got := m.Len("k"); got != 1000 {
		t.Fatalf("entry count: actual=%d expected=1000", got)
	}
}

func Test_F38_ContextCancellationIsHonoured(t *testing.T) {
	t.Parallel()
	m := NewMemory(10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Append(ctx, "k", Item{Kind: KindUser}); err == nil {
		t.Fatalf("Append with cancelled ctx: actual=nil expected=error")
	}
	if _, err := m.Messages(ctx, "k"); err == nil {
		t.Fatalf("Messages with cancelled ctx: actual=nil expected=error")
	}
}

func Test_F38_FileHistoryRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	f := NewFile(path, 100)
	ctx := context.Background()

	for _, it := range []Item{{Kind: KindUser, Content: "u1"}, call("c1"), result("c1")} {
		if err := f.Append(ctx, "group:1", it); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := f.Append(ctx, "group:2", Item{Kind: KindUser, Content: "other"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := f.Messages(ctx, "group:1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 3 || got[0].Content != "u1" || got[1].ToolCalls[0].ID != "c1" || got[2].ToolCallID != "c1" {
		t.Fatalf("file round trip: %+v", got)
	}
	if Orphans(got) != 0 {
		t.Fatalf("file history has orphans")
	}
	other, _ := f.Messages(ctx, "group:2")
	if len(other) != 1 || other[0].Content != "other" {
		t.Fatalf("key isolation: %+v", other)
	}
}

func Test_F38_FileHistoryTrimAndReset(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	f := NewFile(path, 100)
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if err := f.Append(ctx, "k", Item{Kind: KindUser, Content: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := f.Append(ctx, "keep", Item{Kind: KindUser, Content: "keepme"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := f.Trim(ctx, "k", 2); err != nil {
		t.Fatalf("Trim: %v", err)
	}
	got, _ := f.Messages(ctx, "k")
	if len(got) != 2 || got[1].Content != "m5" {
		t.Fatalf("after Trim: actual=%+v", got)
	}
	kept, _ := f.Messages(ctx, "keep")
	if len(kept) != 1 || kept[0].Content != "keepme" {
		t.Fatalf("Trim clobbered another key: %+v", kept)
	}

	if err := f.Reset(ctx, "k"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got, _ = f.Messages(ctx, "k")
	if len(got) != 0 {
		t.Fatalf("after Reset: actual=%d expected=0", len(got))
	}
	kept, _ = f.Messages(ctx, "keep")
	if len(kept) != 1 {
		t.Fatalf("Reset clobbered another key: %+v", kept)
	}
}
