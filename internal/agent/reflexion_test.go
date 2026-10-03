package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// echoAgent 原样返回 Query，且完全无状态，用于并发测试。
type echoAgent struct{}

func (echoAgent) Run(ctx context.Context, in Input) (*Output, error) {
	return &Output{Text: in.Query, LLMCalls: 1, Steps: []Step{{Type: StepThought, Content: in.Query}}}, nil
}

// scriptedBase 按序返回脚本化的初稿或错误。
type scriptedBase struct {
	mu    sync.Mutex
	outs  []string
	errs  []error
	calls int
}

func (s *scriptedBase) Run(ctx context.Context, in Input) (*Output, error) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.mu.Unlock()

	if i < len(s.errs) && s.errs[i] != nil {
		return &Output{Steps: []Step{{Type: StepObservation, Error: s.errs[i].Error()}}}, s.errs[i]
	}
	text := ""
	if i < len(s.outs) {
		text = s.outs[i]
	}
	return &Output{Text: text, LLMCalls: 1, Usage: llm.Usage{TotalTokens: 10}, Steps: []Step{{Type: StepThought, Content: text}}}, nil
}

func (s *scriptedBase) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func stepsContain(out *Output, substr string) bool {
	if out == nil {
		return false
	}
	for _, st := range out.Steps {
		if strings.Contains(st.Content, substr) {
			return true
		}
	}
	return false
}

// Test_F36_EvaluatorPassOnlyCallsLLMOnce：达标时只调用 1 次 LLM（验收）。
func Test_F36_EvaluatorPassOnlyCallsLLMOnce(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{
		Response: &llm.ChatResponse{Content: "draft", FinishReason: "stop", Usage: llm.Usage{TotalTokens: 3}},
	})
	a := &ReflexionAgent{
		Base: &DirectAgent{LLM: fake, Assembler: testAssembler("sys")},
		Config: ReflexionConfig{Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
			return 1, "达标", nil
		})},
	}
	out, err := a.Run(context.Background(), Input{Query: "q"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.Calls() != 1 {
		t.Fatalf("evaluator passed but LLM calls=%d, want 1", fake.Calls())
	}
	if out.Text != "draft" {
		t.Fatalf("text=%q want draft", out.Text)
	}
}

// Test_F36_ReflectionRetryImprovesDraft：未达标时反思并重试，反思记 Step 与 usage。
func Test_F36_ReflectionRetryImprovesDraft(t *testing.T) {
	t.Parallel()
	base := &scriptedBase{outs: []string{"bad", "good"}}
	a := &ReflexionAgent{
		Base: base,
		Config: ReflexionConfig{
			MaxReflections: 1,
			Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
				if out.Text == "good" {
					return 1, "好", nil
				}
				return 0, "还差一点", nil
			}),
		},
	}
	out, err := a.Run(context.Background(), Input{Query: "原始任务"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.Calls() != 2 {
		t.Fatalf("base calls=%d want 2 (draft + retry)", base.Calls())
	}
	if out.Text != "good" {
		t.Fatalf("text=%q want good", out.Text)
	}
	if !stepsContain(out, "[反思]") {
		t.Fatalf("reflection step missing: %+v", out.Steps)
	}
	if !stepsContain(out, "还差一点") {
		t.Fatalf("reflection text must carry the evaluator reason: %+v", out.Steps)
	}
	if out.LLMCalls != 2 {
		t.Fatalf("LLMCalls=%d want 2 (merged)", out.LLMCalls)
	}
}

// Test_F36_EvaluatorErrorDegradesToDraft：评估失败必须返回初稿而不是整体失败。
func Test_F36_EvaluatorErrorDegradesToDraft(t *testing.T) {
	t.Parallel()
	boom := errors.New("evaluator down")
	base := &scriptedBase{outs: []string{"draft"}}
	var warned []string
	a := &ReflexionAgent{
		Base: base,
		Config: ReflexionConfig{Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
			return 0, "", boom
		})},
		Warn: func(s string) { warned = append(warned, s) },
	}
	out, err := a.Run(context.Background(), Input{Query: "q"})
	if err != nil {
		t.Fatalf("evaluator failure must not fail the run: %v", err)
	}
	if out.Text != "draft" {
		t.Fatalf("text=%q want draft", out.Text)
	}
	if base.Calls() != 1 {
		t.Fatalf("base calls=%d want 1", base.Calls())
	}
	if len(warned) == 0 {
		t.Fatalf("evaluator failure must warn (not silent)")
	}
}

// Test_F36_RetryFailureDegradesToDraft：重试本身失败时降级为当前草稿。
func Test_F36_RetryFailureDegradesToDraft(t *testing.T) {
	t.Parallel()
	boom := errors.New("retry down")
	base := &scriptedBase{outs: []string{"draft"}, errs: []error{nil, boom}}
	var warned []string
	a := &ReflexionAgent{
		Base: base,
		Config: ReflexionConfig{
			MaxReflections: 1,
			Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
				return 0, "不行", nil
			}),
		},
		Warn: func(s string) { warned = append(warned, s) },
	}
	out, err := a.Run(context.Background(), Input{Query: "q"})
	if err != nil {
		t.Fatalf("retry failure must degrade, not fail: %v", err)
	}
	if out.Text != "draft" {
		t.Fatalf("text=%q want draft", out.Text)
	}
	if base.Calls() != 2 {
		t.Fatalf("base calls=%d want 2", base.Calls())
	}
	if len(warned) == 0 {
		t.Fatalf("retry failure must warn")
	}
}

// Test_F36_MaxReflectionsBoundsRetries：反思次数上限可配。
func Test_F36_MaxReflectionsBoundsRetries(t *testing.T) {
	t.Parallel()
	base := &scriptedBase{}
	a := &ReflexionAgent{
		Base: base,
		Config: ReflexionConfig{
			MaxReflections: 3,
			Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
				return 0, "永不达标", nil
			}),
		},
	}
	if _, err := a.Run(context.Background(), Input{Query: "q"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.Calls() != 4 {
		t.Fatalf("base calls=%d want 4 (1 draft + 3 reflections)", base.Calls())
	}
}

// Test_F36_ConcurrentRunsKeepReflectionLocal：并发 50 个 Run 的反思内容不串（-race）。
func Test_F36_ConcurrentRunsKeepReflectionLocal(t *testing.T) {
	t.Parallel()
	shared := &ReflexionAgent{
		Base: echoAgent{},
		Config: ReflexionConfig{
			MaxReflections: 1,
			Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
				return 0, out.Text, nil
			}),
		},
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("token-%d-end", i)
			out, err := shared.Run(context.Background(), Input{Query: token})
			errs[i] = err
			if err != nil {
				return
			}
			if !stepsContain(out, token) {
				t.Errorf("run %d lost its own reflection token %q; steps=%+v", i, token, out.Steps)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

// Test_F36_TokenBudgetStopsReflections：token 预算耗尽即停止反思（预算可配）。
func Test_F36_TokenBudgetStopsReflections(t *testing.T) {
	t.Parallel()
	base := &scriptedBase{}
	a := &ReflexionAgent{
		Base: base,
		Config: ReflexionConfig{
			MaxReflections: 5,
			MaxTokens:      15,
			Evaluator: EvaluatorFunc(func(ctx context.Context, in Input, out *Output) (float64, string, error) {
				return 0, "永不达标", nil
			}),
		},
	}
	if _, err := a.Run(context.Background(), Input{Query: "q"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.Calls() != 2 {
		t.Fatalf("base calls=%d want 2 (budget stops after the first retry)", base.Calls())
	}
}

// Test_F36_NoBaseAgentIsReported：缺依赖时返回错误而不是 panic。
func Test_F36_NoBaseAgentIsReported(t *testing.T) {
	t.Parallel()
	var a *ReflexionAgent
	out, err := a.Run(context.Background(), Input{Query: "q"})
	if !errors.Is(err, ErrNoBaseAgent) {
		t.Fatalf("err=%v want ErrNoBaseAgent", err)
	}
	if out == nil || len(out.Steps) == 0 {
		t.Fatalf("Steps must be filled on the error path")
	}
}
