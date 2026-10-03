package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// Orchestrator-Workers（F-37）相关的默认值。
const (
	// DefaultMaxWorkers 是同一层内并行执行的最大子任务数。
	DefaultMaxWorkers = 4
	// DefaultSubtaskTimeout 是单个子任务的独立超时。
	DefaultSubtaskTimeout = 30 * time.Second
	// DefaultSummaryBudget 是喂给汇总 LLM 的字符（rune）预算。
	DefaultSummaryBudget = 8000
	// DefaultMaxSubtasks 是单次规划允许的最大子任务数（防规划产物失控）。
	DefaultMaxSubtasks = 8
	// FinishReasonOrchestrated 是编排完成时的结束原因。
	FinishReasonOrchestrated = "orchestrated"
)

// F-37 的哨兵错误。
var (
	// ErrNoWorkers 表示编排器没有配置任何 worker。
	ErrNoWorkers = errors.New("orchestrator has no workers")
	// ErrNoWorkerAgent 表示 Worker 的内层 Agent 为空。
	ErrNoWorkerAgent = errors.New("worker has no agent")
	// ErrInvalidPlan 表示规划产物无法解码。
	ErrInvalidPlan = errors.New("invalid plan")
)

// 规划与汇总的系统提示词默认值。
const (
	// DefaultPlannerPrompt 要求模型只输出一个 JSON 计划对象。
	DefaultPlannerPrompt = "你是任务规划器。把用户任务拆成若干可独立执行的子任务。" +
		"只输出一个 JSON 对象，字段：analysis（字符串）、subtasks（数组，每项含 id、description、worker_type、input）、" +
		"dependencies（对象，键为子任务 id，值为其依赖的 id 数组）。不要输出任何解释。"
	// DefaultSummaryPrompt 要求模型综合子任务结果给出最终回答。
	DefaultSummaryPrompt = "你是结果汇总者。下面按子任务给出执行结果，请综合它们回答用户的原始任务。" +
		"结果相互矛盾时要明确指出，缺失的结果要说明缺失，不要编造。"
)

// Subtask 是一个可独立执行的子任务。
type Subtask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	WorkerType  string `json:"worker_type"`
	Input       string `json:"input"`
}

// Plan 是规划产物。
//
// 规划产物一律视为**不可信输入**：依赖图可能成环（F-37 要求回退串行）。
type Plan struct {
	Analysis     string              `json:"analysis"`
	Subtasks     []Subtask           `json:"subtasks"`
	Dependencies map[string][]string `json:"dependencies"`
}

// Worker 是一个可被编排的执行单元。
//
// 字段 agent 不导出，外部用 NewWorker 构造；SystemPrompt 会在调用内层 Agent 前
// 追加到 query 前面（Agent 契约只暴露 Query，没有独立的 system 入参）。
type Worker struct {
	Name         string
	SystemPrompt string
	Tools        []string
	agent        Agent
}

// NewWorker 构造一个 Worker。
func NewWorker(name string, a Agent) Worker {
	return Worker{Name: name, agent: a}
}

// Run 实现 Agent：先应用 Worker 的 SystemPrompt，再委托给内层 Agent。
func (w Worker) Run(ctx context.Context, in Input) (*Output, error) {
	out := &Output{}
	if w.agent == nil {
		out.AddStep(Step{Type: StepObservation, Error: ErrNoWorkerAgent.Error()})
		return out, ErrNoWorkerAgent
	}
	if p := strings.TrimSpace(w.SystemPrompt); p != "" {
		in.Query = p + "\n\n" + in.Query
	}
	return w.agent.Run(ctx, in)
}

// SubtaskResult 是单个子任务的执行结果。
type SubtaskResult struct {
	ID      string
	Subtask Subtask
	Worker  string
	Output  *Output
	Text    string
	Success bool
	Skipped bool
	// SkipReason 说明为何被跳过（依赖失败或依赖被跳过）。
	SkipReason string
	Error      string
	DurationMS int64
}

// Orchestration 是编排的完整可观测结果。
type Orchestration struct {
	Output         *Output
	Plan           Plan
	Results        []SubtaskResult
	SerialFallback bool
}

// OrchestratorConfig 配置 Orchestrator。
type OrchestratorConfig struct {
	// MaxWorkers 是同层并行上限；<=0 时取 DefaultMaxWorkers。
	MaxWorkers int
	// SubtaskTimeout 是每个子任务的独立超时；<=0 时取 DefaultSubtaskTimeout。
	SubtaskTimeout time.Duration
	// SummaryBudget 是汇总输入的 rune 预算；<=0 时取 DefaultSummaryBudget。
	SummaryBudget int
	// MaxSubtasks 是规划允许的最大子任务数；<=0 时取 DefaultMaxSubtasks。
	MaxSubtasks int
	// PlannerPrompt / SummaryPrompt 为空时取对应默认值。
	PlannerPrompt string
	SummaryPrompt string
}

// Orchestrator 实现 F-37：规划 → 拓扑分层 → 同层并行 → 汇总。
//
// 它本身实现 Agent，因此可以被上层当作任意范式使用；需要拿到子任务级结果时
// 调用 RunDetailed。所有运行状态都是 Run 内的局部变量，编排器结构体是只读配置。
type Orchestrator struct {
	// LLM 是规划与汇总用的模型。
	LLM llm.LLM
	// Workers 按 WorkerType 索引。
	Workers map[string]Worker
	// DefaultWorker 是 WorkerType 未命中时的回退 key。
	DefaultWorker string
	// Config 见 OrchestratorConfig。
	Config OrchestratorConfig
	// Warn 接收降级告警（规划失败、成环、worker 失败、汇总失败都不静默）。
	Warn func(string)
	// Now 注入时间源（测试用）。
	Now func() time.Time
}

// Run 实现 Agent；只返回汇总后的 Output。
func (o *Orchestrator) Run(ctx context.Context, in Input) (*Output, error) {
	orch, err := o.RunDetailed(ctx, in)
	if orch != nil && orch.Output != nil {
		return orch.Output, err
	}
	return &Output{}, err
}

// RunDetailed 执行一次编排并返回子任务级明细。
func (o *Orchestrator) RunDetailed(ctx context.Context, in Input) (*Orchestration, error) {
	out := &Output{}
	empty := &Orchestration{Output: out}
	if o == nil || o.LLM == nil {
		out.AddStep(Step{Type: StepObservation, Error: ErrNoLLM.Error()})
		return empty, ErrNoLLM
	}
	if len(o.Workers) == 0 {
		out.AddStep(Step{Type: StepObservation, Error: ErrNoWorkers.Error()})
		return empty, ErrNoWorkers
	}

	plan, usage, calls, planErr := o.plan(ctx, in)
	if planErr != nil {
		// 规划失败不判死：退化为"整任务交给一个 worker"。
		o.warnf("orchestrator planning failed: %v; falling back to a single subtask", planErr)
		plan = Plan{
			Analysis: "规划失败，退化为单任务",
			Subtasks: []Subtask{{ID: "task-1", Description: in.Query, WorkerType: o.DefaultWorker, Input: in.Query}},
		}
	}
	plan = normalizePlan(plan, in.Query, o.DefaultWorker, o.maxSubtasks())
	out.Usage = addUsage(out.Usage, usage)
	out.LLMCalls += calls
	out.AddStep(Step{Type: StepThought, Content: "[规划] " + plan.Analysis})

	orch := &Orchestration{Plan: plan}
	results := make(map[string]*SubtaskResult, len(plan.Subtasks))
	var mu sync.Mutex

	layers, cyclic := topoLayers(plan)
	if cyclic {
		orch.SerialFallback = true
		o.warnf("plan dependency graph has a cycle; falling back to serial execution")
		out.AddStep(Step{Type: StepObservation, Content: "依赖图存在环，回退为串行执行"})
		for _, st := range plan.Subtasks {
			if skip, reason := dependencyBlocked(st.ID, plan.Dependencies, results, &mu); skip {
				mu.Lock()
				results[st.ID] = &SubtaskResult{ID: st.ID, Subtask: st, Skipped: true, SkipReason: reason}
				mu.Unlock()
				continue
			}
			r := o.runSubtask(ctx, in, st)
			mu.Lock()
			results[st.ID] = r
			mu.Unlock()
		}
	} else {
		for _, layer := range layers {
			o.runLayer(ctx, in, layer, plan.Dependencies, results, &mu)
		}
	}

	// 结果按规划顺序输出：并行不改变可复现性。
	for _, st := range plan.Subtasks {
		mu.Lock()
		r := results[st.ID]
		mu.Unlock()
		if r == nil {
			r = &SubtaskResult{ID: st.ID, Subtask: st, Skipped: true, SkipReason: "未执行"}
		}
		orch.Results = append(orch.Results, *r)
		out.Steps = append(out.Steps, resultStep(r))
		if r.Output != nil {
			out.Usage = addUsage(out.Usage, r.Output.Usage)
			out.LLMCalls += r.Output.LLMCalls
			out.ToolCalls = append(out.ToolCalls, r.Output.ToolCalls...)
		}
	}

	text, sumUsage, sumCalls, sumErr := o.summarize(ctx, in, orch.Results)
	out.Usage = addUsage(out.Usage, sumUsage)
	out.LLMCalls += sumCalls
	if sumErr != nil {
		o.warnf("orchestrator summary failed: %v; degrading to concatenated results", sumErr)
		out.AddStep(Step{Type: StepObservation, Content: "汇总失败，返回拼接结果", Error: sumErr.Error()})
	} else {
		out.AddStep(Step{Type: StepThought, Content: "[汇总] " + text})
	}
	out.Text = text
	out.FinishReason = FinishReasonOrchestrated
	orch.Output = out
	return orch, nil
}

// plan 调 LLM 产出计划。
func (o *Orchestrator) plan(ctx context.Context, in Input) (Plan, llm.Usage, int, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: o.plannerPrompt()},
		{Role: llm.RoleUser, Content: in.Query},
	}
	resp, err := o.LLM.Chat(ctx, &llm.ChatRequest{Messages: messages})
	if err != nil {
		return Plan{}, llm.Usage{}, 0, fmt.Errorf("plan: %w", err)
	}
	if resp == nil {
		return Plan{}, llm.Usage{}, 0, ErrInvalidPlan
	}
	p, err := parsePlan(resp.Content)
	if err != nil {
		return Plan{}, resp.Usage, 1, err
	}
	return p, resp.Usage, 1, nil
}

// parsePlan 从模型输出里解出计划；允许代码块围栏与前后解释文字。
func parsePlan(content string) (Plan, error) {
	var p Plan
	body := strings.TrimSpace(stripCodeFence(strings.TrimSpace(content)))
	raw := extractJSONObject(body)
	if raw == "" {
		return p, fmt.Errorf("%w: no JSON object", ErrInvalidPlan)
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("%w: %w", ErrInvalidPlan, err)
	}
	if len(p.Subtasks) == 0 {
		return p, fmt.Errorf("%w: no subtasks", ErrInvalidPlan)
	}
	return p, nil
}

// extractJSONObject 取第一个 { 到最后一个 } 之间的片段。
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// normalizePlan 清洗不可信的规划产物：补 ID/输入、去重、丢弃未知依赖、限制数量。
func normalizePlan(p Plan, fallbackQuery, defaultWorker string, maxSubtasks int) Plan {
	out := Plan{Analysis: p.Analysis, Dependencies: map[string][]string{}}
	ids := make(map[string]bool, len(p.Subtasks))
	for _, st := range p.Subtasks {
		id := strings.TrimSpace(st.ID)
		if id == "" {
			id = fmt.Sprintf("task-%d", len(out.Subtasks)+1)
		}
		if ids[id] {
			continue
		}
		ids[id] = true
		st.ID = id
		if strings.TrimSpace(st.Input) == "" {
			st.Input = st.Description
		}
		if strings.TrimSpace(st.Input) == "" {
			st.Input = fallbackQuery
		}
		if st.WorkerType == "" {
			st.WorkerType = defaultWorker
		}
		out.Subtasks = append(out.Subtasks, st)
		if maxSubtasks > 0 && len(out.Subtasks) >= maxSubtasks {
			break
		}
	}
	for id, deps := range p.Dependencies {
		if !ids[id] {
			continue
		}
		for _, dep := range deps {
			if ids[dep] {
				out.Dependencies[id] = append(out.Dependencies[id], dep)
			}
		}
	}
	return out
}

// topoLayers 用 Kahn 算法把 DAG 分层；返回 cyclic=true 表示有环。
//
// 分层内保持规划顺序，保证同一份计划每次得到完全相同的层与顺序。
func topoLayers(plan Plan) (layers [][]Subtask, cyclic bool) {
	byID := make(map[string]Subtask, len(plan.Subtasks))
	order := make([]string, 0, len(plan.Subtasks))
	for _, st := range plan.Subtasks {
		if _, dup := byID[st.ID]; dup {
			continue
		}
		byID[st.ID] = st
		order = append(order, st.ID)
	}
	indeg := make(map[string]int, len(order))
	adj := make(map[string][]string, len(order))
	for _, id := range order {
		indeg[id] = 0
	}
	for _, id := range order {
		for _, dep := range plan.Dependencies[id] {
			if _, ok := byID[dep]; !ok {
				continue
			}
			adj[dep] = append(adj[dep], id)
			indeg[id]++
		}
	}
	remaining := make(map[string]int, len(indeg))
	for id, n := range indeg {
		remaining[id] = n
	}
	done := 0
	for done < len(order) {
		var layer []Subtask
		for _, id := range order {
			if remaining[id] == 0 {
				layer = append(layer, byID[id])
			}
		}
		if len(layer) == 0 {
			return nil, true
		}
		for _, st := range layer {
			remaining[st.ID] = -1
			done++
			for _, next := range adj[st.ID] {
				remaining[next]--
			}
		}
		layers = append(layers, layer)
	}
	return layers, false
}

// runLayer 并行执行同一层；信号量把并发限制在 MaxWorkers。
func (o *Orchestrator) runLayer(ctx context.Context, in Input, layer []Subtask, deps map[string][]string, results map[string]*SubtaskResult, mu *sync.Mutex) {
	sem := make(chan struct{}, o.maxWorkers())
	var wg sync.WaitGroup
	for _, st := range layer {
		if skip, reason := dependencyBlocked(st.ID, deps, results, mu); skip {
			mu.Lock()
			results[st.ID] = &SubtaskResult{ID: st.ID, Subtask: st, Skipped: true, SkipReason: reason}
			mu.Unlock()
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(st Subtask) {
			defer wg.Done()
			defer func() { <-sem }()
			r := o.runSubtask(ctx, in, st)
			mu.Lock()
			results[st.ID] = r
			mu.Unlock()
		}(st)
	}
	wg.Wait()
}

// runSubtask 用独立超时执行一个子任务；任何失败都转成 Result，不上抛。
func (o *Orchestrator) runSubtask(ctx context.Context, in Input, st Subtask) *SubtaskResult {
	res := &SubtaskResult{ID: st.ID, Subtask: st}
	w, ok := o.workerFor(st.WorkerType)
	if !ok {
		res.Error = "no worker configured for type " + st.WorkerType
		return res
	}
	res.Worker = w.Name

	tctx, cancel := context.WithTimeout(ctx, o.subtaskTimeout())
	defer cancel()

	sub := Input{Query: st.Input, History: in.History, SessionKey: in.SessionKey, Files: in.Files, Role: in.Role}
	start := o.now()
	out, err := w.Run(tctx, sub)
	res.DurationMS = o.now().Sub(start).Milliseconds()
	res.Output = out
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if out != nil {
		res.Text = out.Text
	}
	res.Success = true
	return res
}

// summarize 调 LLM 综合结果；失败时退化为已截断的拼接文本。
func (o *Orchestrator) summarize(ctx context.Context, in Input, results []SubtaskResult) (string, llm.Usage, int, error) {
	payload := truncateResults(results, o.summaryBudget())
	if strings.TrimSpace(payload) == "" {
		return "没有可汇总的子任务结果。", llm.Usage{}, 0, nil
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: o.summaryPrompt()},
		{Role: llm.RoleUser, Content: payload},
	}
	resp, err := o.LLM.Chat(ctx, &llm.ChatRequest{Messages: messages})
	if err != nil {
		return payload, llm.Usage{}, 0, fmt.Errorf("summarize: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return payload, llm.Usage{}, 0, nil
	}
	return resp.Content, resp.Usage, 1, nil
}

// truncateResults 把成功结果拼成汇总输入，并保证不超过 rune 预算。
func truncateResults(results []SubtaskResult, budget int) string {
	if budget <= 0 {
		budget = DefaultSummaryBudget
	}
	var b strings.Builder
	used := 0
	truncated := false
	for i := range results {
		r := &results[i]
		if !r.Success {
			continue
		}
		if used >= budget {
			truncated = true
			break
		}
		entry := "### " + r.ID + "\n" + strings.TrimSpace(r.Text) + "\n\n"
		runes := []rune(entry)
		left := budget - used
		if len(runes) > left {
			runes = runes[:left]
			truncated = true
		}
		b.WriteString(string(runes))
		used += len(runes)
	}
	out := b.String()
	if truncated {
		marker := "\n[已按汇总预算截断]"
		if runes := []rune(out); len(runes)+len([]rune(marker)) <= budget {
			out += marker
		}
	}
	if runes := []rune(out); len(runes) > budget {
		out = string(runes[:budget])
	}
	return out
}

// dependencyBlocked 判断某子任务的依赖里是否有失败/被跳过的；未知依赖忽略。
func dependencyBlocked(id string, deps map[string][]string, results map[string]*SubtaskResult, mu *sync.Mutex) (bool, string) {
	mu.Lock()
	defer mu.Unlock()
	for _, dep := range deps[id] {
		r, ok := results[dep]
		if !ok || r == nil {
			continue
		}
		if r.Skipped {
			return true, "依赖 " + dep + " 被跳过"
		}
		if !r.Success {
			return true, "依赖 " + dep + " 失败"
		}
	}
	return false, ""
}

// resultStep 把一个子任务结果转成可观测 Step。
func resultStep(r *SubtaskResult) Step {
	s := Step{Type: StepObservation, Content: r.ID, DurationMS: r.DurationMS}
	switch {
	case r.Skipped:
		s.Error = "skipped: " + r.SkipReason
	case !r.Success:
		s.Error = r.Error
	case r.Text != "":
		s.ToolName = r.Worker
		s.ToolOutput = r.Text
	}
	return s
}

func (o *Orchestrator) workerFor(typ string) (Worker, bool) {
	if typ != "" {
		if w, ok := o.Workers[typ]; ok {
			return w, true
		}
	}
	if o.DefaultWorker != "" {
		if w, ok := o.Workers[o.DefaultWorker]; ok {
			return w, true
		}
	}
	return Worker{}, false
}

func (o *Orchestrator) maxWorkers() int {
	if o.Config.MaxWorkers > 0 {
		return o.Config.MaxWorkers
	}
	return DefaultMaxWorkers
}

func (o *Orchestrator) subtaskTimeout() time.Duration {
	if o.Config.SubtaskTimeout > 0 {
		return o.Config.SubtaskTimeout
	}
	return DefaultSubtaskTimeout
}

func (o *Orchestrator) summaryBudget() int {
	if o.Config.SummaryBudget > 0 {
		return o.Config.SummaryBudget
	}
	return DefaultSummaryBudget
}

func (o *Orchestrator) maxSubtasks() int {
	if o.Config.MaxSubtasks > 0 {
		return o.Config.MaxSubtasks
	}
	return DefaultMaxSubtasks
}

func (o *Orchestrator) plannerPrompt() string {
	if p := strings.TrimSpace(o.Config.PlannerPrompt); p != "" {
		return p
	}
	return DefaultPlannerPrompt
}

func (o *Orchestrator) summaryPrompt() string {
	if p := strings.TrimSpace(o.Config.SummaryPrompt); p != "" {
		return p
	}
	return DefaultSummaryPrompt
}

func (o *Orchestrator) warnf(format string, args ...any) {
	if o.Warn != nil {
		o.Warn(fmt.Sprintf(format, args...))
	}
}

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

var _ Agent = (*Orchestrator)(nil)
var _ Agent = Worker{}
