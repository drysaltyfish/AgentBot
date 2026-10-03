package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// fenceMark 是 Markdown 代码块围栏。
const fenceMark = "```"

type stubAgent struct {
	out *Output
	err error
}

func (s *stubAgent) Run(ctx context.Context, in Input) (*Output, error) { return s.out, s.err }

// runViaHandler 代表"上层 Handler"：它只依赖 Agent 接口。
func runViaHandler(ctx context.Context, a Agent, query string) (string, int, error) {
	out, err := a.Run(ctx, Input{Query: query, SessionKey: session.Key{GroupID: 1}})
	if err != nil {
		return "", 0, err
	}
	return out.Text, len(out.Steps), nil
}

func Test_F34_ImplementationsAreInterchangeable(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Response: &llm.ChatResponse{Content: "hello", FinishReason: "stop"}})
	direct := &DirectAgent{LLM: fake, Assembler: testAssembler("sys")}
	other := &stubAgent{out: &Output{Text: "hi", Steps: []Step{{Type: StepThought}}}}

	text, steps, err := runViaHandler(context.Background(), direct, "q")
	if err != nil || text != "hello" || steps != 1 {
		t.Fatalf("direct: actual=(%q,%d,%v)", text, steps, err)
	}
	text, steps, err = runViaHandler(context.Background(), other, "q")
	if err != nil || text != "hi" || steps != 1 {
		t.Fatalf("stub: actual=(%q,%d,%v)", text, steps, err)
	}

	req := fake.LastRequest()
	if req.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("system prompt not prepended: %+v", req.Messages)
	}
	if req.Messages[len(req.Messages)-1].Content != "q" {
		t.Fatalf("query not appended: %+v", req.Messages)
	}
}

func Test_F34_NilLLMIsReportedNotPanicked(t *testing.T) {
	t.Parallel()
	var a *DirectAgent
	out, err := a.Run(context.Background(), Input{Query: "x"})
	if !errors.Is(err, ErrNoLLM) {
		t.Fatalf("nil agent: actual=%v expected=ErrNoLLM", err)
	}
	if out == nil {
		t.Fatalf("output must not be nil even on error")
	}
	b := &DirectAgent{Assembler: testAssembler("")}
	if _, err := b.Run(context.Background(), Input{Query: "x"}); !errors.Is(err, ErrNoLLM) {
		t.Fatalf("agent without llm: actual=%v expected=ErrNoLLM", err)
	}
}

func Test_F34_StepsAreFilledOnErrorPath(t *testing.T) {
	t.Parallel()
	boom := errors.New("llm down")
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Err: boom})
	out, err := (&DirectAgent{LLM: fake, Assembler: testAssembler("")}).Run(context.Background(), Input{Query: "x"})
	if !errors.Is(err, boom) {
		t.Fatalf("err: actual=%v expected=%v", err, boom)
	}
	if len(out.Steps) == 0 || out.Steps[0].Error == "" {
		t.Fatalf("steps must be filled on the error path: %+v", out.Steps)
	}
}

func Test_F34_ToolCallsOnlyResultIsValid(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Response: &llm.ChatResponse{
		ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "weather", Arguments: "{}"}},
		FinishReason: llm.FinishReasonToolCalls,
	}})
	out, err := (&DirectAgent{LLM: fake, Assembler: testAssembler("")}).Run(context.Background(), Input{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text != "" || len(out.ToolCalls) != 1 {
		t.Fatalf("tool-call-only result must be valid: %+v", out)
	}
}

func Test_F34_ContextCancellationReturnsQuickly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Delay: time.Hour, Response: &llm.ChatResponse{Content: "late"}})
	cancel()
	start := time.Now()
	if _, err := (&DirectAgent{LLM: fake, Assembler: testAssembler("")}).Run(ctx, Input{Query: "x"}); err == nil {
		t.Fatalf("Run with cancelled ctx: actual=nil expected=error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation not honoured: %v", elapsed)
	}
}

func fencedSample(payload string) string {
	return fenceMark + "json\n" + payload + "\n" + fenceMark
}

func Test_F39_ParsesTheEightShapes(t *testing.T) {
	t.Parallel()
	clean := `{"action":"send_msg","params":{"text":"hi"}}`
	cases := []struct {
		name        string
		in          string
		wantActions int
		wantWarn    bool
		wantErr     bool
	}{
		{"clean json", clean, 1, false, false},
		{"fenced", fencedSample(clean), 1, false, false},
		{"multi object", `{"action":"a"}{"action":"b"}`, 2, false, false},
		{"trailing comma", `{"action":"a",}{"action":"b"}`, 1, true, false},
		{"truncated", `{"action":"a","params":{"text":"unterminated`, 0, true, true},
		{"pure text", `just talking, no json here`, 0, true, true},
		{"nested object", `{"action":"a","params":{"deep":{"x":[1,2,{"y":3}]}}}`, 1, false, false},
		{"huge integer", `{"action":"a","params":{"qq":1234567890123456789}}`, 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ParseActions(tc.in, 0)
			if tc.wantErr && !errors.Is(err, ErrNoActions) {
				t.Fatalf("err: actual=%v expected=ErrNoActions", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("err: actual=%v expected=nil", err)
			}
			if len(res.Actions) != tc.wantActions {
				t.Fatalf("actions: actual=%d expected=%d (%+v)", len(res.Actions), tc.wantActions, res.Actions)
			}
			if tc.wantWarn && len(res.Warnings) == 0 {
				t.Fatalf("expected warnings, got none")
			}
			if !tc.wantWarn && len(res.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", res.Warnings)
			}
		})
	}
}

func Test_F39_HugeIntegersKeepPrecision(t *testing.T) {
	t.Parallel()
	res, err := ParseActions(`{"action":"kick","params":{"qq":1234567890123456789}}`, 0)
	if err != nil {
		t.Fatalf("ParseActions: %v", err)
	}
	got, ok := res.Actions[0].ParamInt64("qq")
	if !ok {
		t.Fatalf("qq param not an int: %+v", res.Actions[0].Params)
	}
	if got != 1234567890123456789 {
		t.Fatalf("precision lost: actual=%d expected=1234567890123456789", got)
	}
	if raw := res.Actions[0].ParamRaw("qq"); raw != "1234567890123456789" {
		t.Fatalf("raw param: actual=%s", raw)
	}
}

func Test_F39_SkipsEmptyActionAndBadParams(t *testing.T) {
	t.Parallel()
	res, err := ParseActions(`{"action":""}{"action":"ok"}{"action":"bad","params":"not-an-object"}`, 0)
	if err != nil {
		t.Fatalf("ParseActions: %v", err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Name != "ok" {
		t.Fatalf("actions: %+v", res.Actions)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings: actual=%v expected 2", res.Warnings)
	}
}

func Test_F39_ActionLimitTruncates(t *testing.T) {
	t.Parallel()
	one := `{"action":"a"}`
	res, err := ParseActions(strings.Repeat(one, 30), 5)
	if err != nil {
		t.Fatalf("ParseActions: %v", err)
	}
	if len(res.Actions) != 5 {
		t.Fatalf("actions: actual=%d expected=5", len(res.Actions))
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "limit") {
		t.Fatalf("truncation not reported: %v", res.Warnings)
	}
}

func Test_F39_NoActionsOnEmptyInput(t *testing.T) {
	t.Parallel()
	res, err := ParseActions("   ", 0)
	if !errors.Is(err, ErrNoActions) {
		t.Fatalf("empty input: actual=%v expected=ErrNoActions", err)
	}
	if res == nil {
		t.Fatalf("result must not be nil")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte("{}"), &obj); err != nil {
		t.Fatalf("sanity: %v", err)
	}
}
