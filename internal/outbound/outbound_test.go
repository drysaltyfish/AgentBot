package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

type recordingCaller struct {
	mu   sync.Mutex
	reqs []transport.Request
}

func (r *recordingCaller) Call(ctx context.Context, req transport.Request) (transport.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return transport.Response{RetCode: 0, Data: json.RawMessage(`{"message_id":7}`)}, nil
}

func (r *recordingCaller) last() transport.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func (r *recordingCaller) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func sentMessage(t *testing.T, req transport.Request) event.Message {
	t.Helper()
	raw, err := json.Marshal(req.Params["message"])
	if err != nil {
		t.Fatalf("marshal message param: %v", err)
	}
	msg, _, err := event.ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse sent message: %v", err)
	}
	return msg
}

var messy = "  hello   \n\n\n\n  world  "

func Test_F55_SendsGoThroughTheChain(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	var audits []AuditRecord
	s := NewSender(caller, New(), WithAudit(func(r AuditRecord) { audits = append(audits, r) }))

	id, err := s.Send(context.Background(), GroupTarget(123), event.Message{event.Text(messy)})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id.Int64() != 7 {
		t.Fatalf("message id: actual=%d expected=7", id.Int64())
	}
	if caller.count() != 1 {
		t.Fatalf("caller requests: actual=%d expected=1", caller.count())
	}
	got := sentMessage(t, caller.last()).PlainText()
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("blank-line denoise did not run: %q", got)
	}
	if strings.HasSuffix(got, " ") || strings.HasSuffix(got, "\n") {
		t.Fatalf("tail trim did not run: %q", got)
	}
	if len(audits) != 1 || audits[0].Filtered != got {
		t.Fatalf("audit record missing or wrong: %+v", audits)
	}
}

func Test_F55_LengthLimitTruncatesAndMarks(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	s := NewSender(caller, New(WithMaxLength(20)))

	long := strings.Repeat("字", 100)
	if _, err := s.Send(context.Background(), GroupTarget(1), event.Message{event.Text(long)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := sentMessage(t, caller.last()).PlainText()
	if len([]rune(got)) > 20 {
		t.Fatalf("length limit not enforced: actual=%d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "已截断") {
		t.Fatalf("truncation marker missing: %q", got)
	}
}

func Test_F55_PanickingFilterPassesThroughOriginal(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	var panicked string
	chain := New(
		WithFilter(FilterSensitive, func(string) string { panic("filter exploded") }),
		WithPanicHook(func(name string, recovered any) { panicked = name }),
	)
	chain.Enable(FilterLength, false)
	s := NewSender(caller, chain)

	if _, err := s.Send(context.Background(), GroupTarget(1), event.Message{event.Text("keep me")}); err != nil {
		t.Fatalf("Send must succeed even when a filter panics: %v", err)
	}
	if panicked != FilterSensitive {
		t.Fatalf("panic hook: actual=%q expected=%q", panicked, FilterSensitive)
	}
	if got := sentMessage(t, caller.last()).PlainText(); got != "keep me" {
		t.Fatalf("original content was not passed through: %q", got)
	}
}

func Test_F55_ChainOrderIsFixed(t *testing.T) {
	t.Parallel()
	var order []string
	mark := func(name string) Filter {
		return func(s string) string {
			order = append(order, name)
			return s
		}
	}
	chain := New(
		WithFilter(FilterLength, mark("length")),
		WithFilter(FilterSensitive, mark("sensitive")),
		WithFilter(FilterDenoise, mark("denoise")),
		WithFilter(FilterTextReplace, mark("replace")),
		WithFilter(FilterTrimTail, mark("trim")),
		WithFilter(FilterNormalize, mark("normalize")),
	)
	chain.Apply("x")
	want := "length,sensitive,denoise,replace,trim,normalize"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("chain order: actual=%s expected=%s", got, want)
	}
}

func Test_F55_DisabledFilterIsSkipped(t *testing.T) {
	t.Parallel()
	chain := New()
	if got := chain.Apply("a\n\n\n\nb"); strings.Contains(got, "\n\n\n") {
		t.Fatalf("denoise should be on by default: %q", got)
	}
	chain.Enable(FilterDenoise, false)
	if got := chain.Apply("a\n\n\n\nb"); !strings.Contains(got, "\n\n\n") {
		t.Fatalf("disabled denoise still ran: %q", got)
	}
}

func Test_F55_BuiltinFiltersAreIdempotent(t *testing.T) {
	t.Parallel()
	chain := New(WithMaxLength(40))
	samples := []string{
		"  a  \n\n\n\n  b  ",
		strings.Repeat("x", 100),
		"plain",
		"",
		"中文\n\n\n\n中文",
	}
	for _, in := range samples {
		once := chain.Apply(in)
		twice := chain.Apply(once)
		if once != twice {
			t.Fatalf("filter is not idempotent for %q: once=%q twice=%q", in, once, twice)
		}
	}
}

func Test_F55_EmptyAfterFilteringIsNotSentButAudited(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	var audits []AuditRecord
	s := NewSender(caller, New(), WithAudit(func(r AuditRecord) { audits = append(audits, r) }))

	_, err := s.Send(context.Background(), GroupTarget(1), event.Message{event.Text("   \n\n\n   ")})
	if !errors.Is(err, ErrEmptyAfterFilter) {
		t.Fatalf("Send: actual=%v expected=ErrEmptyAfterFilter", err)
	}
	if caller.count() != 0 {
		t.Fatalf("empty message must not be sent: actual=%d calls", caller.count())
	}
	if len(audits) != 1 || !audits[0].Dropped {
		t.Fatalf("dropped message must be audited: %+v", audits)
	}
}

func Test_F55_NoTargetIsRejected(t *testing.T) {
	t.Parallel()
	s := NewSender(&recordingCaller{}, New())
	if _, err := s.Send(context.Background(), Target{}, event.Message{event.Text("hi")}); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("empty target: actual=%v expected=ErrNoTarget", err)
	}
}

func Test_F55_TextReplacementRunsInStableOrder(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	chain := New(WithFilter(FilterTextReplace, ReplaceText(map[string]string{"aa": "b", "b": "c"})))
	chain.Enable(FilterLength, false)
	s := NewSender(caller, chain)

	if _, err := s.Send(context.Background(), GroupTarget(1), event.Message{event.Text("aa")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := sentMessage(t, caller.last()).PlainText(); got != "c" {
		t.Fatalf("replacement result: actual=%q expected=%q", got, "c")
	}
}

func Test_F55_NonTextSegmentsPassThrough(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	s := NewSender(caller, New())
	msg := event.Message{event.At("10001"), event.Text("  hi  ")}
	if _, err := s.Send(context.Background(), GroupTarget(1), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := sentMessage(t, caller.last())
	if len(sent) != 2 || sent[0].Type != event.TypeAt || sent[0].Data["qq"] != "10001" {
		t.Fatalf("non-text segment was dropped or altered: %+v", sent)
	}
	// 去噪只做右侧裁剪与空行折叠，保留前导缩进。
	if sent[1].Data["text"] != "  hi" {
		t.Fatalf("text segment not filtered: actual=%q expected=%q", sent[1].Data["text"], "  hi")
	}
}

func Test_SplitParagraphsSplitsOnBlankLinesOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single paragraph", "就一句话", []string{"就一句话"}},
		{"single newline is not a split", "第一行\n第二行", []string{"第一行\n第二行"}},
		{"one blank line", "第一段\n\n第二段", []string{"第一段", "第二段"}},
		{"many blank lines collapse", "第一段\n\n\n\n第二段", []string{"第一段", "第二段"}},
		{"crlf", "第一段\r\n\r\n第二段", []string{"第一段", "第二段"}},
		{"leading and trailing blanks", "\n\n第一段\n\n第二段\n\n", []string{"第一段", "第二段"}},
		{"whitespace-only lines", "第一段\n   \n第二段", []string{"第一段", "第二段"}},
		{"three paragraphs", "一\n\n二\n\n三", []string{"一", "二", "三"}},
		{"empty", "", nil},
		{"only blanks", "\n\n   \n", nil},
	}
	for _, tc := range cases {
		got := SplitParagraphs(tc.in, 0)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %d parts %q, want %d", tc.name, len(got), got, len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: part %d = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// Test_SplitParagraphsMergesTailBeyondMax 保证不会一次刷屏。
func Test_SplitParagraphsMergesTailBeyondMax(t *testing.T) {
	t.Parallel()
	in := "一\n\n二\n\n三\n\n四\n\n五\n\n六"
	got := SplitParagraphs(in, 3)
	if len(got) != 3 {
		t.Fatalf("segments: got %d %q, want 3", len(got), got)
	}
	if got[0] != "一" || got[1] != "二" {
		t.Fatalf("head segments changed: %q", got[:2])
	}
	if !strings.Contains(got[2], "三") || !strings.Contains(got[2], "六") {
		t.Fatalf("tail must be merged into the last segment: %q", got[2])
	}
}

func Test_SplitParagraphsDefaultMaxWhenNonPositive(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 0; i < DefaultMaxSegments+3; i++ {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("段")
	}
	if got := SplitParagraphs(b.String(), 0); len(got) != DefaultMaxSegments {
		t.Fatalf("non-positive max must fall back to DefaultMaxSegments: got %d", len(got))
	}
}

// Test_SendManyKeepsOrderAndPaces 覆盖连发的顺序与间隔。
func Test_SendManyKeepsOrderAndPaces(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	s := NewSender(caller, New())

	parts := []string{"第一条", "第二条", "第三条"}
	start := time.Now()
	sent, err := s.SendMany(context.Background(), PrivateTarget(42), parts, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("SendMany: %v", err)
	}
	if sent != len(parts) {
		t.Fatalf("sent=%d want=%d", sent, len(parts))
	}
	if caller.count() != len(parts) {
		t.Fatalf("caller saw %d calls, want %d", caller.count(), len(parts))
	}
	// 顺序必须保持：这是聊天里"一条一条发"的基本要求。
	for i, part := range parts {
		got := sentMessage(t, caller.reqs[i]).PlainText()
		if got != part {
			t.Fatalf("message %d out of order: got %q want %q", i, got, part)
		}
	}
	if elapsed := time.Since(start); elapsed < 2*30*time.Millisecond {
		t.Fatalf("pacing not applied: elapsed=%v", elapsed)
	}
}

// Test_SendManyStopsOnContextCancel 保证连发等待是 ctx 感知的。
func Test_SendManyStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	s := NewSender(caller, New())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	sent, err := s.SendMany(ctx, PrivateTarget(42), []string{"一", "二", "三", "四"}, time.Second)
	if err == nil {
		t.Fatalf("cancelled context must abort the pacing wait")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error should wrap context.Canceled: %v", err)
	}
	if sent != 1 {
		t.Fatalf("only the first message should have been sent: sent=%d", sent)
	}
}
