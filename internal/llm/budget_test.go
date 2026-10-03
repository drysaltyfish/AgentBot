package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func Test_F32_EstimateTokensHeuristic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"中文", 2},   // 2 个汉字 -> ceil(2/1.5)=2
		{"中文中", 2},  // 3 个汉字 -> 2
		{"中文中文", 3}, // 4 个汉字 -> ceil(4/1.5)=3
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Fatalf("EstimateTokens(%q): actual=%d expected=%d", tc.text, got, tc.want)
		}
	}
}

func Test_F32_HeuristicCounterHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (HeuristicCounter{}).Count(ctx, "m", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("actual=%v expected context.Canceled", err)
	}
}

func longHistory(n int) []Message {
	msgs := make([]Message, 0, n+1)
	msgs = append(msgs, Message{Role: RoleSystem, Content: strings.Repeat("keep me ", 5), Pinned: true})
	for i := 0; i < n; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf("turn %d %s", i, strings.Repeat("x", 80))})
	}
	return msgs
}

func estimatedCount(t *testing.T, msgs []Message) int {
	t.Helper()
	n, err := (HeuristicCounter{}).Count(context.Background(), "", msgs)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func Test_F32_FitTrimsToBudget(t *testing.T) {
	t.Parallel()
	msgs := longHistory(20)
	b := Budget{MaxContext: 320, ReserveOutput: 40, ReserveTools: 20}
	got := b.Fit(context.Background(), msgs)
	if len(got) == 0 || len(got) >= len(msgs) {
		t.Fatalf("Fit must trim something but keep content: %d -> %d", len(msgs), len(got))
	}
	if count := estimatedCount(t, got); count > b.InputLimit() {
		t.Fatalf("over budget after Fit: %d > %d", count, b.InputLimit())
	}
	if !got[0].Pinned || got[0].Content != msgs[0].Content {
		t.Fatalf("pinned system must be preserved: %+v", got[0])
	}
	// 未超预算时不得重写（保住前缀缓存）。
	full := b.Fit(context.Background(), msgs[:2])
	if len(full) != 2 {
		t.Fatalf("under-budget messages must pass through unchanged: %d", len(full))
	}
}

func Test_F32_PinnedMessageSurvivesTrimInTheMiddle(t *testing.T) {
	t.Parallel()
	msgs := longHistory(6)
	pinned := Message{Role: RoleSystem, Content: "PINNED-MID", Pinned: true}
	msgs = append(msgs, pinned)
	msgs = append(msgs, longHistory(10)[1:]...)
	b := Budget{MaxContext: 260, ReserveOutput: 30, ReserveTools: 10}
	got := b.Fit(context.Background(), msgs)
	found := false
	for _, m := range got {
		if m.Content == "PINNED-MID" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pinned message in the middle was dropped")
	}
	if estimatedCount(t, got) > b.InputLimit() {
		t.Fatalf("over budget after Fit")
	}
}

func assertToolPairsAligned(t *testing.T, msgs []Message) {
	t.Helper()
	owners := map[string]bool{}
	for _, m := range msgs {
		if m.Role == RoleTool && !owners[m.ToolCallID] {
			t.Fatalf("orphan tool result %q", m.ToolCallID)
		}
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				owners[tc.ID] = true
			}
		}
	}
	results := map[string]bool{}
	for _, m := range msgs {
		if m.Role == RoleTool {
			results[m.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		if m.Role != RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !results[tc.ID] {
				t.Fatalf("dangling tool call %q (result was dropped)", tc.ID)
			}
		}
	}
}

func Test_F32_FitKeepsToolCallPairsTogether(t *testing.T) {
	t.Parallel()
	msgs := []Message{{Role: RoleSystem, Content: "sys", Pinned: true}}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: "tool", Arguments: `{"x":1}`}}},
			Message{Role: RoleTool, ToolCallID: id, Content: strings.Repeat("r", 120)},
		)
	}
	b := Budget{MaxContext: 150, ReserveOutput: 10, ReserveTools: 10}
	got := b.Fit(context.Background(), msgs)
	assertToolPairsAligned(t, got)
	if estimatedCount(t, got) > b.InputLimit() {
		t.Fatalf("over budget after Fit")
	}
}

func Test_F32_FitRequestCountsToolTokens(t *testing.T) {
	t.Parallel()
	tools := []ToolSpec{{
		Name:        "big",
		Description: strings.Repeat("d", 400),
		Parameters:  json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
	}}
	b := Budget{MaxContext: 220, ReserveOutput: 20, ReserveTools: 0}
	msgs := longHistory(20)
	req := &ChatRequest{Messages: msgs, Tools: tools}
	out := b.FitRequest(context.Background(), req)
	if len(req.Messages) != len(msgs) {
		t.Fatalf("FitRequest must not mutate the caller request")
	}
	toolTokens, err := b.ToolTokens(context.Background(), tools)
	if err != nil {
		t.Fatalf("ToolTokens: %v", err)
	}
	if toolTokens <= 0 {
		t.Fatalf("tool tokens must be counted")
	}
	limit := b.MaxContext - b.ReserveOutput - toolTokens
	if got := estimatedCount(t, out.Messages); got > limit {
		t.Fatalf("tool schema not counted in budget: %d > %d", got, limit)
	}
}

type fakeSummarizer struct {
	calls int
	err   error
}

func (f *fakeSummarizer) Summarize(ctx context.Context, dropped []Message) (Message, bool, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, false, err
	}
	f.calls++
	if f.err != nil {
		return Message{}, false, f.err
	}
	return Message{Role: RoleSystem, Content: "SUMMARY", Pinned: true}, true, nil
}

func Test_F32_SummarizeSeamInsertsSummary(t *testing.T) {
	t.Parallel()
	s := &fakeSummarizer{}
	b := Budget{MaxContext: 260, ReserveOutput: 30, ReserveTools: 10, Summarizer: s}
	got := b.Fit(context.Background(), longHistory(20))
	if s.calls != 1 {
		t.Fatalf("summarizer calls: actual=%d expected=1", s.calls)
	}
	if len(got) < 2 || got[1].Content != "SUMMARY" {
		t.Fatalf("summary must sit right after the pinned prefix: %+v", got)
	}
	if estimatedCount(t, got) > b.InputLimit() {
		t.Fatalf("over budget after summary")
	}
}

func Test_F32_SummarizeFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	boom := errors.New("summarize exploded")
	s := &fakeSummarizer{err: boom}
	var reported []error
	b := Budget{
		MaxContext: 260, ReserveOutput: 30, ReserveTools: 10,
		Summarizer: s,
		OnError:    func(err error) { reported = append(reported, err) },
	}
	got := b.Fit(context.Background(), longHistory(20))
	if len(reported) != 1 || !errors.Is(reported[0], boom) {
		t.Fatalf("non-fatal error must be reported: %v", reported)
	}
	for _, m := range got {
		if m.Content == "SUMMARY" {
			t.Fatalf("failed summarizer must not inject a summary")
		}
	}
	if estimatedCount(t, got) > b.InputLimit() {
		t.Fatalf("over budget after fallback trim")
	}
}

func Test_F32_UsageTrackerAggregates(t *testing.T) {
	t.Parallel()
	tr := NewUsageTracker()
	tr.Record("a", 10, Usage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, PromptCacheHitTokens: 6, PromptCacheMissTokens: 2})
	tr.Record("a", 10, Usage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5})
	tr.Record("b", 0, Usage{PromptTokens: 3, CompletionTokens: 3, TotalTokens: 6})

	g := tr.Global()
	if g.Calls != 3 || g.PromptTokens != 15 || g.CompletionTokens != 6 || g.TotalTokens != 21 {
		t.Fatalf("global stats: %+v", g)
	}
	if got := tr.Session("a"); got.Calls != 2 || got.PromptTokens != 12 {
		t.Fatalf("session stats: %+v", got)
	}
	if got := tr.Session("b"); got.Calls != 1 || got.PromptTokens != 3 {
		t.Fatalf("session b stats: %+v", got)
	}
	ratio, samples := tr.Calibration()
	if samples != 2 {
		t.Fatalf("calibration samples: actual=%d expected=2", samples)
	}
	if ratio < 0.599 || ratio > 0.601 {
		t.Fatalf("calibration ratio: actual=%v expected=0.6", ratio)
	}
	if len(tr.Sessions()) != 2 {
		t.Fatalf("sessions copy: %+v", tr.Sessions())
	}
}

func Test_F32_UsageTrackerConcurrent(t *testing.T) {
	t.Parallel()
	tr := NewUsageTracker()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				tr.Record("s", 10, Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
			}
		}()
	}
	wg.Wait()
	if got := tr.Global().Calls; got != 400 {
		t.Fatalf("concurrent global calls: actual=%d expected=400", got)
	}
	if got := tr.Session("s").Calls; got != 400 {
		t.Fatalf("concurrent session calls: actual=%d expected=400", got)
	}
}

func Test_F32_MeasuredCounterPrefersRealUsage(t *testing.T) {
	t.Parallel()
	fallback := CounterFunc(func(ctx context.Context, model string, msgs []Message) (int, error) {
		return 999, nil
	})
	mc := NewMeasuredCounter(fallback)
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	ctx := context.Background()
	if n, err := mc.Count(ctx, "m", msgs); err != nil || n != 999 {
		t.Fatalf("fallback count: n=%d err=%v", n, err)
	}
	mc.Observe("m", msgs, Usage{PromptTokens: 7})
	if n, err := mc.Count(ctx, "m", msgs); err != nil || n != 7 {
		t.Fatalf("real usage must win: n=%d err=%v", n, err)
	}
	// 不同模型/消息指纹不得串味。
	if n, _ := mc.Count(ctx, "other", msgs); n != 999 {
		t.Fatalf("model must participate in the fingerprint: n=%d", n)
	}
}

func Benchmark_F32_Fit(b *testing.B) {
	msgs := longHistory(50)
	budget := Budget{MaxContext: 600, ReserveOutput: 50, ReserveTools: 50}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = budget.Fit(ctx, msgs)
	}
}
