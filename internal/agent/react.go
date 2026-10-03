package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

var (
	// ErrMaxIterations 表示循环达到上限仍未收敛（Steps 仍会完整返回）。
	ErrMaxIterations = errors.New("react loop reached max iterations")
	// ErrNoToolRegistry 表示没有配置工具注册表。
	ErrNoToolRegistry = errors.New("react agent has no tool registry")
)

// Protocol 决定工具调用的协议（见 docs/adr/0001-tool-call-protocol.md）。
type Protocol string

const (
	// ProtocolNative 只消费 provider 原生的 tool_calls。
	ProtocolNative Protocol = "native"
	// ProtocolAuto 以原生为主，文本动作仅作抢救通道。
	ProtocolAuto Protocol = "auto"
)

// 默认值。
const (
	// DefaultMaxIterations 是 ReAct 循环的默认上限。
	DefaultMaxIterations = 10
	// DefaultStepTimeout 是单个工具执行的默认超时。
	DefaultStepTimeout = 30 * time.Second
	// FinishReasonMaxIterations 是循环达上限时的结束原因。
	FinishReasonMaxIterations = "max_iterations"
	// FinishReasonEndOfTurn 是模型主动结束本轮时的结束原因（伴随 ErrEndOfTurn）。
	FinishReasonEndOfTurn = "end_of_turn"
)

// ReactAgent 实现 ReAct 循环（F-35）：先想 → 调工具 → 看结果 → 再想。
//
// 协议选择遵循 ADR-0001：**原生 tool_calls 是唯一执行通道**，F-39 的文本解析器
// 只在模型把动作吐在文本里时作为抢救通道（scavenge）。
type ReactAgent struct {
	LLM          llm.LLM
	Tools        *tool.Registry
	SystemPrompt string

	// MaxIterations <= 0 时取 DefaultMaxIterations。
	MaxIterations int
	// StepTimeout <= 0 时取 DefaultStepTimeout。
	StepTimeout time.Duration
	// ParallelTools 为 true 且同轮所有工具都声明 ConcurrencySafe 时并发执行。
	ParallelTools bool
	// Protocol 为空时按 ProtocolAuto 处理。
	Protocol Protocol
	// Gate 判定工具调用是否需要人工审批（F-45）；为 nil 表示不启用审批。
	Gate Gate
	// Approver 是人工审批通道；判定为 VerdictApprove 但未配置时，调用会被拒绝。
	Approver Approver
	// ApprovalTimeout 是审批等待的独立预算，默认 DefaultApprovalTimeout。
	ApprovalTimeout time.Duration
	// OnApproval 接收审批审计记录（F-60）。
	OnApproval func(ApprovalRecord)
	// Memory 若不为 nil，其结果会被注入到 system 之后、历史之前（见 ADR-0002）。
	// 这样 system 段（所有会话共享的大头）仍然稳定命中缓存。
	Memory Memory
	// Warn 接收降级/抢救告警（不要吞掉，否则会长期掩盖 provider 侧问题）。
	Warn func(string)
	// Now 注入时间源（测试用）。
	Now func() time.Time
}

// Run 实现 Agent。
func (a *ReactAgent) Run(ctx context.Context, in Input) (*Output, error) {
	out := &Output{}
	if a == nil || a.LLM == nil {
		return out, ErrNoLLM
	}
	if a.Tools == nil {
		return out, ErrNoToolRegistry
	}

	messages := make([]llm.Message, 0, len(in.History)+3)
	if a.SystemPrompt != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: a.SystemPrompt})
	}
	// ADR-0002：记忆是独立消息，放在 system 之后、历史之前。
	// 不进 system 是为了保住 system 段的全局缓存；不放到最后是为了让记忆本身也能被缓存。
	if a.Memory != nil {
		items, err := a.Memory.Recall(ctx)
		if err != nil {
			a.warnf("cannot recall memory: %v", err)
		} else if block := RenderMemory(items); block != "" {
			messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: block})
		}
	}
	messages = append(messages, in.History...)
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: in.Query})

	specs := a.Tools.Definitions()
	max := a.maxIterations()

	for i := 0; i < max; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		resp, err := a.LLM.Chat(ctx, &llm.ChatRequest{Messages: messages, Tools: specs})
		if err != nil {
			out.AddStep(Step{Type: StepObservation, Error: err.Error()})
			return out, fmt.Errorf("react iteration %d: %w", i+1, err)
		}
		out.Usage = addUsage(out.Usage, resp.Usage)

		calls := resp.ToolCalls
		if len(calls) == 0 {
			// 抢救通道：模型可能把动作吐在文本里而 tool_calls 为空。
			// 这不是理论风险——DeepSeek 思考模式就会这样。
			calls = a.scavenge(resp.Content)
			if len(calls) == 0 {
				out.Text = resp.Content
				out.FinishReason = resp.FinishReason
				out.AddStep(Step{Type: StepThought, Content: resp.Content})
				return out, nil
			}
		} else if a.protocol() == ProtocolAuto {
			// ADR-0001：原生存在时忽略文本动作，但必须告警，否则会掩盖重复输出。
			if pr, err := ParseActions(resp.Content, 0); err == nil && len(pr.Actions) > 0 {
				a.warnf("iteration %d: native tool_calls present, ignoring %d text action(s)", i+1, len(pr.Actions))
			}
		}

		out.AddStep(Step{Type: StepThought, Content: resp.Content})

		// 关键：assistant 消息必须带上 tool_calls，tool 消息必须带匹配的 tool_call_id，
		// 否则第二轮会被服务端 400 拒绝（反模式 #14）。
		// 思考模式下同时回传 reasoning_content：DeepSeek 要求携带 tools 时必须回传。
		messages = append(messages, llm.Message{
			Role:             llm.RoleAssistant,
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ToolCalls:        calls,
		})

		results := a.executeAll(ctx, calls, in.Role)
		for idx, call := range calls {
			res := results[idx]
			out.ToolCalls = append(out.ToolCalls, call)
			step := Step{
				Type:       StepAction,
				ToolName:   call.Name,
				ToolInput:  call.Arguments,
				ToolOutput: res.String(),
				DurationMS: res.durationMS,
			}
			if res.Failed() {
				step.Error = res.Error
			}
			out.AddStep(step)

			// 工具失败也必须回灌 observation，让模型自行纠错，而不是中断循环。
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    res.String(),
			})

			// F-40：虚拟动作 end_action 结束本轮。用哨兵错误表达控制流，
			// 调用方据此决定"不发送任何消息"，这属于正常收尾而不是失败。
			if res.Metadata[ControlMetadataKey] == ControlEndOfTurn {
				out.FinishReason = FinishReasonEndOfTurn
				out.AddStep(Step{Type: StepObservation, Content: ActionEndTurn})
				return out, ErrEndOfTurn
			}
		}
	}

	out.FinishReason = FinishReasonMaxIterations
	return out, fmt.Errorf("%w: %d（已完成 %d 步）", ErrMaxIterations, max, len(out.Steps))
}

// execution 是带耗时的工具执行结果。
type execution struct {
	tool.Result
	durationMS int64
}

func (a *ReactAgent) maxIterations() int {
	if a.MaxIterations > 0 {
		return a.MaxIterations
	}
	return DefaultMaxIterations
}

func (a *ReactAgent) stepTimeout() time.Duration {
	if a.StepTimeout > 0 {
		return a.StepTimeout
	}
	return DefaultStepTimeout
}

func (a *ReactAgent) protocol() Protocol {
	if a.Protocol == "" {
		return ProtocolAuto
	}
	return a.Protocol
}

func (a *ReactAgent) warnf(format string, args ...any) {
	if a.Warn != nil {
		a.Warn(fmt.Sprintf(format, args...))
	}
}

func (a *ReactAgent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// executeAll 执行一轮里的全部调用，返回值顺序与入参一致。
//
// 默认串行（顺序可预测）；只有 ParallelTools 打开**且**同轮所有工具都声明
// ConcurrencySafe 时才并发——只要其中有一个不安全就整体退回串行。
func (a *ReactAgent) executeAll(ctx context.Context, calls []llm.ToolCall, role Role) []execution {
	out := make([]execution, len(calls))
	if a.ParallelTools && a.allConcurrencySafe(calls) {
		var wg sync.WaitGroup
		for i, call := range calls {
			wg.Add(1)
			go func(i int, call llm.ToolCall) {
				defer wg.Done()
				defer func() {
					// 工具 panic 不得杀死进程；转成失败的 observation 回灌。
					if r := recover(); r != nil {
						out[i] = execution{Result: tool.Failure(fmt.Sprintf("tool panicked: %v", r))}
					}
				}()
				out[i] = a.execute(ctx, call, role)
			}(i, call)
		}
		wg.Wait()
		return out
	}
	for i, call := range calls {
		out[i] = a.execute(ctx, call, role)
	}
	return out
}

func (a *ReactAgent) allConcurrencySafe(calls []llm.ToolCall) bool {
	for _, call := range calls {
		t, ok := a.Tools.Get(call.Name)
		if !ok || !tool.IsConcurrencySafe(t) {
			return false
		}
	}
	return true
}

// execute 执行单个工具调用。任何失败都转成失败结果，不返回 error——
// 循环必须继续，让模型看到错误并自行纠错。
func (a *ReactAgent) execute(ctx context.Context, call llm.ToolCall, role Role) execution {
	start := a.now()
	t, ok := a.Tools.Get(call.Name)
	if !ok {
		return a.finish(start, tool.Failure(fmt.Sprintf("unknown tool %q", call.Name)))
	}

	// F-45：权限判定与人工审批在**单步超时之外**进行。
	// 若把审批塞进 step 超时，"等人确认"会把工具的执行预算耗光，
	// 表现为审批通过后工具立刻超时。
	if res, denied := a.gate(ctx, call, role, start); denied {
		return res
	}

	stepCtx, cancel := context.WithTimeout(ctx, a.stepTimeout())
	defer cancel()

	res, err := t.Execute(stepCtx, json.RawMessage(call.Arguments))
	if err != nil {
		// 超时也要回灌超时信息，而不是让循环静默。
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			return a.finish(start, tool.Failure(fmt.Sprintf("tool %q timed out after %s", call.Name, a.stepTimeout())))
		}
		return a.finish(start, tool.Failure(err.Error()))
	}
	return a.finish(start, res)
}

func (a *ReactAgent) finish(start time.Time, res tool.Result) execution {
	return execution{Result: res, durationMS: a.now().Sub(start).Milliseconds()}
}

// scavenge 是抢救通道：把模型吐在文本里的动作转成 tool_calls。
func (a *ReactAgent) scavenge(content string) []llm.ToolCall {
	if a.protocol() != ProtocolAuto || content == "" {
		return nil
	}
	pr, err := ParseActions(content, 0)
	if err != nil || len(pr.Actions) == 0 {
		return nil
	}
	calls := make([]llm.ToolCall, 0, len(pr.Actions))
	for i, act := range pr.Actions {
		raw, err := json.Marshal(act.Params)
		if err != nil {
			raw = []byte("{}")
		}
		calls = append(calls, llm.ToolCall{
			ID:        fmt.Sprintf("scavenged-%d", i+1),
			Name:      act.Name,
			Arguments: string(raw),
		})
	}
	a.warnf("scavenged %d action(s) from text because tool_calls was empty; the provider may be dropping them", len(calls))
	return calls
}

// addUsage 累计 usage。
func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:          a.PromptTokens + b.PromptTokens,
		CompletionTokens:      a.CompletionTokens + b.CompletionTokens,
		TotalTokens:           a.TotalTokens + b.TotalTokens,
		PromptCacheHitTokens:  a.PromptCacheHitTokens + b.PromptCacheHitTokens,
		PromptCacheMissTokens: a.PromptCacheMissTokens + b.PromptCacheMissTokens,
		ReasoningTokens:       a.ReasoningTokens + b.ReasoningTokens,
	}
}

var _ Agent = (*ReactAgent)(nil)

// approvalTimeout 返回审批等待的独立预算。
func (a *ReactAgent) approvalTimeout() time.Duration {
	if a.ApprovalTimeout > 0 {
		return a.ApprovalTimeout
	}
	return DefaultApprovalTimeout
}

func (a *ReactAgent) auditApproval(rec ApprovalRecord) {
	if a.OnApproval != nil {
		a.OnApproval(rec)
	}
}

// gate 做权限判定与人工审批；返回 (结果, 是否已拒绝)。
//
// 审批等待使用调用方的 ctx（独立预算），而不是单步超时的 ctx——这是 F-45 的明确要求。
func (a *ReactAgent) gate(ctx context.Context, call llm.ToolCall, role Role, start time.Time) (execution, bool) {
	if a.Gate == nil {
		return execution{}, false
	}
	if role == "" {
		role = RoleMember
	}
	req := ApprovalRequest{ToolName: call.Name, Arguments: call.Arguments, Role: role}
	verdict := a.Gate.Check(req)

	// VerdictApprove 与未知判定共用 default 分支（fail-closed），故不单列 case。
	//nolint:exhaustive // 见上：default 已覆盖 VerdictApprove 与未知判定
	switch verdict {
	case VerdictAllow:
		return execution{}, false

	case VerdictDeny:
		a.auditApproval(ApprovalRecord{
			At: a.now(), Request: req, Verdict: verdict,
			Allowed: false, Reason: "策略拒绝", WaitMS: a.now().Sub(start).Milliseconds(),
		})
		return a.finish(start, tool.Failure(fmt.Sprintf("工具 %q 被策略拒绝，未执行", call.Name))), true

	// VerdictApprove 与任何未知判定走同一条 fail-closed 分支，因此不单列 case。
	default:
		if a.Approver == nil {
			a.auditApproval(ApprovalRecord{
				At: a.now(), Request: req, Verdict: verdict,
				Allowed: false, Reason: ErrNoApprover.Error(), WaitMS: a.now().Sub(start).Milliseconds(),
			})
			return a.finish(start, tool.Failure(fmt.Sprintf(
				"工具 %q 需要人工确认，但没有配置审批通道，已拒绝", call.Name))), true
		}

		waitStart := a.now()
		waitCtx, cancel := context.WithTimeout(ctx, a.approvalTimeout())
		defer cancel()

		decision, err := a.Approver.Approve(waitCtx, req)
		waited := a.now().Sub(waitStart).Milliseconds()

		rec := ApprovalRecord{At: a.now(), Request: req, Verdict: verdict, WaitMS: waited}

		switch {
		case err != nil:
			// 超时与取消都按"拒绝"处理：不确定的副作用不该被执行。
			rec.Allowed = false
			rec.TimedOut = errors.Is(err, context.DeadlineExceeded)
			rec.Reason = "审批未通过: " + err.Error()
			a.auditApproval(rec)
			return a.finish(start, tool.Failure(fmt.Sprintf(
				"工具 %q 的审批未通过（%v），已拒绝", call.Name, err))), true

		case !decision.Allowed:
			rec.Allowed = false
			rec.Reason = decision.Reason
			if rec.Reason == "" {
				rec.Reason = "审批被拒绝"
			}
			a.auditApproval(rec)
			return a.finish(start, tool.Failure(fmt.Sprintf(
				"工具 %q 的审批被拒绝：%s", call.Name, rec.Reason))), true
		}

		rec.Allowed = true
		rec.Reason = decision.Reason
		a.auditApproval(rec)
		return execution{}, false
	}
}
