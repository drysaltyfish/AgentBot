package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// textAgent 返回固定文本。
type textAgent struct{ text string }

func (a textAgent) Run(ctx context.Context, in Input) (*Output, error) {
	return &Output{Text: a.text, LLMCalls: 1}, nil
}

// failAgent 总是失败。
type failAgent struct{ err error }

func (a failAgent) Run(ctx context.Context, in Input) (*Output, error) {
	return &Output{Steps: []Step{{Type: StepObservation, Error: a.err.Error()}}}, a.err
}

// agentFunc 把函数适配成 Agent。
type agentFunc func(ctx context.Context, in Input) (*Output, error)

func (f agentFunc) Run(ctx context.Context, in Input) (*Output, error) { return f(ctx, in) }

// gateAgent 要求至少两个同层子任务同时进入，否则等到 ctx 超时。
type gateAgent struct {
	name  string
	count *int32
	gate  chan struct{}
}

func (g *gateAgent) Run(ctx context.Context, in Input) (*Output, error) {
	if atomic.AddInt32(g.count, 1) == 2 {
		close(g.gate)
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &Output{Text: g.name, LLMCalls: 1}, nil
}

func orchestratorLLM(t *testing.T, plan Plan, summary string) *llm.FakeLLM {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	return llm.NewFakeLLM(
		llm.ScriptedResponse{Response: &llm.ChatResponse{Content: string(b), FinishReason: "stop"}},
		llm.ScriptedResponse{Response: &llm.ChatResponse{Content: summary, FinishReason: "stop"}},
	)
}

func diamondPlan() Plan {
	return Plan{
		Analysis: "diamond",
		Subtasks: []Subtask{
			{ID: "A", Description: "first", WorkerType: "t", Input: "do A"},
			{ID: "B", Description: "left", WorkerType: "b", Input: "do B"},
			{ID: "C", Description: "right", WorkerType: "c", Input: "do C"},
			{ID: "D", Description: "join", WorkerType: "t", Input: "do D"},
		},
		Dependencies: map[string][]string{"B": {"A"}, "C": {"A"}, "D": {"B", "C"}},
	}
}

func resultIDs(results []SubtaskResult) []string {
	out := make([]string, 0, len(results))
	for i := range results {
		out = append(out, results[i].ID)
	}
	return out
}

// Test_F37_DiamondDependenciesRunInParallel：菱形依赖中 B 与 C 并行（验收）。
func Test_F37_DiamondDependenciesRunInParallel(t *testing.T) {
	t.Parallel()
	var count int32
	gate := make(chan struct{})
	b := &gateAgent{name: "B", count: &count, gate: gate}
	c := &gateAgent{name: "C", count: &count, gate: gate}
	o := &Orchestrator{
		LLM: orchestratorLLM(t, diamondPlan(), "final"),
		Workers: map[string]Worker{
			"t": NewWorker("t", textAgent{text: "t"}),
			"b": NewWorker("b", b),
			"c": NewWorker("c", c),
		},
		Config: OrchestratorConfig{SubtaskTimeout: 2 * time.Second, SummaryBudget: 1000},
	}
	orch, err := o.RunDetailed(context.Background(), Input{Query: "task"})
	if err != nil {
		t.Fatalf("RunDetailed: %v", err)
	}
	if orch.SerialFallback {
		t.Fatalf("diamond plan must not fall back to serial")
	}
	if atomic.LoadInt32(&count) != 2 {
		t.Fatalf("parallel entries=%d want 2", count)
	}
	if len(orch.Results) != 4 {
		t.Fatalf("results=%d want 4", len(orch.Results))
	}
	if !orch.Results[1].Success || !orch.Results[2].Success {
		t.Fatalf("B/C must both succeed when parallel: B=%+v C=%+v", orch.Results[1], orch.Results[2])
	}
	want := []string{"A", "B", "C", "D"}
	got := resultIDs(orch.Results)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result order=%v want %v", got, want)
		}
	}
	if orch.Output.Text != "final" {
		t.Fatalf("text=%q want final", orch.Output.Text)
	}
}

// Test_F37_CycleFallsBackToSerial：成环不死锁，回退串行（验收）。
func Test_F37_CycleFallsBackToSerial(t *testing.T) {
	t.Parallel()
	plan := Plan{
		Analysis: "cycle",
		Subtasks: []Subtask{
			{ID: "A", WorkerType: "t", Input: "a"},
			{ID: "B", WorkerType: "t", Input: "b"},
		},
		Dependencies: map[string][]string{"A": {"B"}, "B": {"A"}},
	}
	o := &Orchestrator{
		LLM:     orchestratorLLM(t, plan, "final"),
		Workers: map[string]Worker{"t": NewWorker("t", textAgent{text: "t"})},
		Config:  OrchestratorConfig{SubtaskTimeout: time.Second, SummaryBudget: 1000},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	orch, err := o.RunDetailed(ctx, Input{Query: "task"})
	if err != nil {
		t.Fatalf("RunDetailed: %v", err)
	}
	if !orch.SerialFallback {
		t.Fatalf("cycle must fall back to serial")
	}
	if len(orch.Results) != 2 || !orch.Results[0].Success || !orch.Results[1].Success {
		t.Fatalf("serial fallback must still run every subtask: %+v", orch.Results)
	}
}

// Test_F37_FailedWorkerSkipsDependents：单个子任务失败不拖垮整体，依赖者标记 skipped。
func Test_F37_FailedWorkerSkipsDependents(t *testing.T) {
	t.Parallel()
	boom := errors.New("worker down")
	plan := Plan{
		Analysis: "chain",
		Subtasks: []Subtask{
			{ID: "A", WorkerType: "fail", Input: "a"},
			{ID: "B", WorkerType: "ok", Input: "b"},
			{ID: "C", WorkerType: "ok", Input: "c"},
		},
		Dependencies: map[string][]string{"B": {"A"}, "C": {"B"}},
	}
	o := &Orchestrator{
		LLM: orchestratorLLM(t, plan, "final"),
		Workers: map[string]Worker{
			"fail": NewWorker("fail", failAgent{err: boom}),
			"ok":   NewWorker("ok", textAgent{text: "ok"}),
		},
		Config: OrchestratorConfig{SubtaskTimeout: time.Second, SummaryBudget: 1000},
	}
	orch, err := o.RunDetailed(context.Background(), Input{Query: "task"})
	if err != nil {
		t.Fatalf("one worker failure must not fail the whole run: %v", err)
	}
	if orch.Results[0].Success {
		t.Fatalf("A must fail")
	}
	if !orch.Results[1].Skipped || !strings.Contains(orch.Results[1].SkipReason, "A") {
		t.Fatalf("B must be skipped because of A: %+v", orch.Results[1])
	}
	if !orch.Results[2].Skipped || !strings.Contains(orch.Results[2].SkipReason, "B") {
		t.Fatalf("C must be skipped because of B: %+v", orch.Results[2])
	}
}

// Test_F37_SummaryInputIsTruncatedToBudget：汇总输入必须截断到预算内。
func Test_F37_SummaryInputIsTruncatedToBudget(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("长", 500)
	plan := Plan{
		Analysis: "one",
		Subtasks: []Subtask{{ID: "A", WorkerType: "t", Input: "a"}},
	}
	fake := orchestratorLLM(t, plan, "final")
	o := &Orchestrator{
		LLM:     fake,
		Workers: map[string]Worker{"t": NewWorker("t", textAgent{text: long})},
		Config:  OrchestratorConfig{SubtaskTimeout: time.Second, SummaryBudget: 40},
	}
	orch, err := o.RunDetailed(context.Background(), Input{Query: "task"})
	if err != nil {
		t.Fatalf("RunDetailed: %v", err)
	}
	reqs := fake.Requests()
	if len(reqs) < 2 {
		t.Fatalf("want plan + summary requests, got %d", len(reqs))
	}
	if len(reqs[1].Messages) == 0 {
		t.Fatalf("summary request has no messages")
	}
	user := reqs[1].Messages[len(reqs[1].Messages)-1]
	if got := len([]rune(user.Content)); got > 40 {
		t.Fatalf("summary input runes=%d exceeds budget 40", got)
	}
	if orch.Output.Text != "final" {
		t.Fatalf("text=%q want final", orch.Output.Text)
	}
}

// Test_F37_ResultOrderIsPlanOrder：独立子任务并行执行后结果顺序仍按规划顺序。
func Test_F37_ResultOrderIsPlanOrder(t *testing.T) {
	t.Parallel()
	plan := Plan{Analysis: "many"}
	want := []string{}
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		plan.Subtasks = append(plan.Subtasks, Subtask{ID: id, WorkerType: "t", Input: id})
		want = append(want, id)
	}
	for round := 0; round < 2; round++ {
		o := &Orchestrator{
			LLM:     orchestratorLLM(t, plan, "final"),
			Workers: map[string]Worker{"t": NewWorker("t", textAgent{text: "t"})},
			Config:  OrchestratorConfig{SubtaskTimeout: time.Second, SummaryBudget: 1000},
		}
		orch, err := o.RunDetailed(context.Background(), Input{Query: "task"})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		got := resultIDs(orch.Results)
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("round %d order=%v want %v", round, got, want)
			}
		}
	}
}

// Test_F37_WorkerPrependsSystemPrompt：Worker 把 SystemPrompt 追加到 query 前。
func Test_F37_WorkerPrependsSystemPrompt(t *testing.T) {
	t.Parallel()
	var got string
	inner := agentFunc(func(ctx context.Context, in Input) (*Output, error) {
		got = in.Query
		return &Output{Text: "ok"}, nil
	})
	w := NewWorker("w", inner)
	w.SystemPrompt = "你是检索专员"
	if _, err := w.Run(context.Background(), Input{Query: "任务正文"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(got, "你是检索专员") || !strings.Contains(got, "任务正文") {
		t.Fatalf("system prompt not applied: %q", got)
	}
}

// Test_F37_NoWorkersIsReported：没有 worker 时返回错误而不是 panic。
func Test_F37_NoWorkersIsReported(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{LLM: llm.NewFakeLLM()}
	out, err := o.Run(context.Background(), Input{Query: "q"})
	if !errors.Is(err, ErrNoWorkers) {
		t.Fatalf("err=%v want ErrNoWorkers", err)
	}
	if out == nil || len(out.Steps) == 0 {
		t.Fatalf("Steps must be filled on the error path")
	}
}
