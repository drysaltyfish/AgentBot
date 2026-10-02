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
	Text         string
	Steps        []Step
	Usage        llm.Usage
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
	out.ToolCalls = resp.ToolCalls
	out.FinishReason = resp.FinishReason
	out.AddStep(Step{Type: StepThought, Content: resp.Content})
	return out, nil
}

var _ Agent = (*DirectAgent)(nil)
