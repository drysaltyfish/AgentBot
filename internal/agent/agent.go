// Package agent 定义统一 Agent 契约与动作流解析（FEATURES.md F-34 / F-39）。
package agent

import (
	"context"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// Step 类型常量。
const (
	StepThought     = "thought"
	StepAction      = "action"
	StepObservation = "observation"
)

// Attachment 是用户随消息带来的附件。
type Attachment struct {
	Name string
	MIME string
	Data []byte
}

// Input 是一次 Agent 运行的输入。
//
// 注意：**不设** Metadata map[string]any——需要传递的字段必须显式出现在结构体上，
// 否则会出现"写了读不到"的死字段（反模式 #8）。
type Input struct {
	Query      string
	History    []llm.Message
	SessionKey session.Key
	Files      []Attachment
	// Role 是发起者的角色，供 F-45 的权限判定使用；为空时按 RoleMember 处理。
	Role Role
}

// Step 是可观测的一步。
type Step struct {
	Type       string
	Content    string
	ToolName   string
	ToolInput  string
	ToolOutput string
	DurationMS int64
	Error      string
}

// Output 是一次 Agent 运行的结果。
type Output struct {
	Text  string
	Steps []Step
	// Usage 是本次运行**所有** LLM 调用的用量合计。
	Usage llm.Usage
	// LLMCalls 是本次运行实际发生的 LLM 调用次数（ReAct 每轮一次）。
	// 台账需要"请求数"而不是"步数"：步数把思考与动作分开计，不是请求数。
	LLMCalls int
	// PromptDigest 是本次运行**第一次** LLM 调用实际发送的消息序列的逐条指纹（F-89）。
	//
	// 取第一次而不是最后一次：跨轮比较的对象应当是"这一轮拿到的输入"。
	// 带工具的轮次最后几次调用含 tool 消息，而下一轮的首调没有——两边形状不同，
	// 拿来比会稳定地误报为"前缀分歧"（真机实测踩过）。
	//
	// 只记指纹不记正文：正文可能含隐私，而前缀稳定性只需要判断"这一段是否相同"。
	PromptDigest []string
	// MemoryDigest 是注入的记忆块的指纹（无记忆时为空）。
	//
	// 记忆块按 ADR-0002 位于 system 之后，记忆一变它**之后**的内容全部失效。
	// 那是**预期**变化，必须能与意外变化区分开，否则每次都误报。
	MemoryDigest string
	ToolCalls    []llm.ToolCall
	FinishReason string
}

// AddStep 追加一步。
func (o *Output) AddStep(s Step) {
	o.Steps = append(o.Steps, s)
}

// Agent 是所有范式共用的出口。
type Agent interface {
	Run(ctx context.Context, in Input) (*Output, error)
}

// DirectAgent 是最小的 Agent 实现：把提示词交给 LLM 直接作答，不调用工具。
//
// 它同时是"两种实现可互换"这一验收条件的第二个实现。
type DirectAgent struct {
	LLM          llm.LLM
	SystemPrompt string
}

// Run 实现 Agent。
func (a *DirectAgent) Run(ctx context.Context, in Input) (*Output, error) {
	out := &Output{}
	if a == nil || a.LLM == nil {
		return out, ErrNoLLM
	}
	messages := make([]llm.Message, 0, len(in.History)+2)
	if a.SystemPrompt != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: a.SystemPrompt})
	}
	messages = append(messages, in.History...)
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: in.Query})

	resp, err := a.LLM.Chat(ctx, &llm.ChatRequest{Messages: messages})
	if err != nil {
		out.AddStep(Step{Type: StepObservation, Error: err.Error()})
		return out, err
	}
	out.Text = resp.Content
	out.Usage = resp.Usage
	out.LLMCalls = 1
	out.PromptDigest = llm.Digest(messages)
	out.ToolCalls = resp.ToolCalls
	out.FinishReason = resp.FinishReason
	out.AddStep(Step{Type: StepThought, Content: resp.Content})
	return out, nil
}

var _ Agent = (*DirectAgent)(nil)
