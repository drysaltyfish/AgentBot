// Package agent 定义统一 Agent 契约与动作流解析（FEATURES.md F-34 / F-39）。
package agent

import (
	"context"
	"errors"

	"github.com/drysaltyfish/agentbot/internal/history"
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
	Query string
	// History 是按原顺序追加的历史条目（含发言人、时间与环境消息标记）。
	//
	// 这里收**条目**而不是成品消息：环境消息的压缩与呈现窗口都发生在
	// 装配层，agent 拿到的是已经压好的消息序列。
	History    []history.Item
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

// StreamingAgent 是 Agent 的**可选**能力：生成过程中把增量交给调用方（F-64）。
//
// 为什么只有部分实现提供它：ReAct 的每一轮都在等完整的工具调用结果，
// 边流边发会让用户先看到半截文本、随后又收到工具调用之后的新文本；
// 而 DirectAgent 一次模型调用就产出最终回答，流出多少就是回答多少。
// 调用方用类型断言决定走哪条路——语义由接口表达，而不是让配置去猜。
type StreamingAgent interface {
	// RunStream 消费模型流：每个分片交给 splitter，返回聚合后的结果。
	RunStream(ctx context.Context, in Input, splitter *llm.StreamSplitter) (*Output, error)
}

// MessageAssembler 把历史条目、记忆块与当前输入装配成一次请求的消息序列。
//
// 定义在 agent 侧，让 agent 只依赖"能装配"这一能力，而不依赖具体实现。
// 布局规则（不可变前缀 / 记忆位置 / 呈现窗口 / 环境消息压缩）由实现拥有，
// 且**只能有一份**（见 docs/adr/0002 与 internal/conversation）。
type MessageAssembler interface {
	// BuildFor 按会话键装配本轮消息序列（半静态段随会话人格变化，F-65/F-82）。
	BuildFor(ctx context.Context, key session.Key, hist []history.Item, memoryBlock, user string) []llm.Message
	PrefixHash() string
}

// ErrNoAssembler 表示没有配置消息装配器。
//
// 装配是必填依赖：缺了它 agent 就只能自己拼消息，而"缓存优先"的布局
// 一旦出现第二份实现，窗口与 token 预算就会静默失效。
var ErrNoAssembler = errors.New("agent has no message assembler")

// DirectAgent 是最小的 Agent 实现：把提示词交给 LLM 直接作答，不调用工具。
//
// 它同时是"两种实现可互换"这一验收条件的第二个实现。
type DirectAgent struct {
	LLM llm.LLM
	// Assembler 是必填的消息装配器：消息布局由它拥有，agent 不自己拼。
	Assembler MessageAssembler
}

// Run 实现 Agent。
func (a *DirectAgent) Run(ctx context.Context, in Input) (*Output, error) {
	out := &Output{}
	if a == nil || a.LLM == nil {
		return out, ErrNoLLM
	}
	if a.Assembler == nil {
		return out, ErrNoAssembler
	}
	messages := a.Assembler.BuildFor(ctx, in.SessionKey, in.History, "", in.Query)

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
var _ StreamingAgent = (*DirectAgent)(nil)

// RunStream 实现 StreamingAgent：边流边发，返回与 Run 同形的结果。
//
// 流式与整段共用同一个装配点（Assembler）：F-64 明确"不要改动请求侧"——
// 消息序列必须与整段路径逐字节相同，否则流式一开就让前缀缓存失效。
func (a *DirectAgent) RunStream(ctx context.Context, in Input, splitter *llm.StreamSplitter) (*Output, error) {
	out := &Output{}
	if a == nil || a.LLM == nil {
		return out, ErrNoLLM
	}
	if a.Assembler == nil {
		return out, ErrNoAssembler
	}
	if splitter == nil {
		// 没给切分器就没有"边"可流：退回整段，而不是报错。
		return a.Run(ctx, in)
	}

	messages := a.Assembler.BuildFor(ctx, in.SessionKey, in.History, "", in.Query)
	ch, err := a.LLM.ChatStream(ctx, &llm.ChatRequest{Messages: messages})
	if err != nil {
		out.AddStep(Step{Type: StepObservation, Error: err.Error()})
		return out, err
	}
	out.LLMCalls = 1
	out.PromptDigest = llm.Digest(messages)

	streamErr := consumeStream(ctx, ch, splitter)
	out.Text = splitter.Full()
	if streamErr != nil {
		out.AddStep(Step{Type: StepObservation, Error: streamErr.Error()})
		return out, streamErr
	}
	out.FinishReason = "stop"
	out.AddStep(Step{Type: StepThought, Content: out.Text})
	return out, nil
}

// consumeStream 顺序消费分片，把"错误"与"结束"都收敛成一次 Finish。
//
// 不能直接用 splitter.ConsumeStream：它会丢掉分片里的 Err（F-28 明确错误也走
// channel），于是"模型中途报错"会表现成"回答提前结束"——用户看到半截话，
// 日志里却没有任何线索。
func consumeStream(ctx context.Context, ch <-chan llm.Chunk, splitter *llm.StreamSplitter) error {
	for {
		select {
		case <-ctx.Done():
			splitter.Finish()
			return ctx.Err()
		case c, ok := <-ch:
			if !ok {
				splitter.Finish()
				return nil
			}
			if c.Err != nil {
				splitter.Finish()
				return c.Err
			}
			splitter.Feed(c)
			if c.Done {
				splitter.Finish()
				return nil
			}
		}
	}
}
