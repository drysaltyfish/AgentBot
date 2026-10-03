package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// Reflexion（F-36）相关的默认值与错误。
const (
	// DefaultMaxReflections 是默认的最大反思-重试次数。
	DefaultMaxReflections = 1
	// DefaultReflexionThreshold 是默认的达标线。
	//
	// score 落在 [0,1]：只有 "score >= Threshold" 才算达标。默认要求满分——
	// 规则评估器通常返回 0/1 的二元判定，满分语义最不容易产生歧义。
	DefaultReflexionThreshold = 1.0
	// DefaultReflectPrompt 是生成反思文本时的系统提示词。
	DefaultReflectPrompt = "你是严格的作答评审员。针对用户任务与未达标的初稿，" +
		"给出简洁、可执行的修改建议。只输出建议本身，不要复述初稿。"
)

// ErrNoBaseAgent 表示 ReflexionAgent 没有配置底层的初稿生成器。
var ErrNoBaseAgent = errors.New("reflexion agent has no base agent")

// Evaluator 对一次运行结果打分（F-36）。
//
// score 越高越好（建议落在 [0,1]），reason 是人类可读的未达标原因；
// 返回 error 表示评估本身失败——此时调用方必须直接返回首次结果，
// 而不是把评估器的故障升级为整次运行的失败。
type Evaluator interface {
	Evaluate(ctx context.Context, in Input, out *Output) (score float64, reason string, err error)
}

// EvaluatorFunc 把普通函数适配成 Evaluator，便于用规则做评估。
type EvaluatorFunc func(ctx context.Context, in Input, out *Output) (score float64, reason string, err error)

// Evaluate 实现 Evaluator。
func (f EvaluatorFunc) Evaluate(ctx context.Context, in Input, out *Output) (float64, string, error) {
	return f(ctx, in, out)
}

// ReflexionConfig 配置 ReflexionAgent。
type ReflexionConfig struct {
	// MaxReflections 是最大反思次数；<=0 时取 DefaultMaxReflections。
	MaxReflections int
	// Evaluator 是评估器；为 nil 时退化为"不反思直接返回初稿"。
	Evaluator Evaluator
	// ReflectPrompt 是生成反思文本的系统提示词；为空时取 DefaultReflectPrompt。
	ReflectPrompt string
	// Threshold 是达标线；<=0 时取 DefaultReflexionThreshold。
	//
	// 注意：因此无法用 0 表示"任何结果都达标"——那会让规则评估器失去意义。
	Threshold float64
	// MaxTokens 是整次 Run 允许消耗的总 token 预算；<=0 表示不限制。
	// 达到预算即停止反思并返回当前结果（降级而不是报错）。
	MaxTokens int
}

// ReflexionAgent 是 F-36 的组合式实现：初稿 → 评估 → 反思 → 重试。
//
// 它不重写 ReAct，而是**装饰**任意 Agent：Base 通常是 *ReactAgent 或 *DirectAgent。
// 反思状态（轮数、累计 usage、反思文本）全部是 Run 内的局部变量——绝不放在
// 结构体字段上，否则并发请求会串数据（F-36 的硬边界，也是反模式"数据竞争"）。
type ReflexionAgent struct {
	// Base 是必填的初稿生成器。
	Base Agent
	// LLM 用于生成反思文本；为 nil 时退化为模板文本（不产生额外调用）。
	LLM llm.LLM
	// Config 配置反思轮数与评估策略。
	Config ReflexionConfig
	// Warn 接收降级告警（评估失败、反思失败、重试失败都不静默）。
	Warn func(string)
	// Now 注入时间源（测试用）。
	Now func() time.Time
}

// Run 实现 Agent。
func (a *ReflexionAgent) Run(ctx context.Context, in Input) (*Output, error) {
	out := &Output{}
	if a == nil || a.Base == nil {
		out.AddStep(Step{Type: StepObservation, Error: ErrNoBaseAgent.Error()})
		return out, ErrNoBaseAgent
	}

	draft, err := a.Base.Run(ctx, in)
	if draft == nil {
		draft = &Output{}
	}
	if err != nil {
		// 初稿本身就失败时照常上抛：调用方拿到的仍是带 Steps 的 out。
		return draft, err
	}
	if a.Config.Evaluator == nil {
		return draft, nil
	}

	threshold := a.threshold()
	max := a.maxReflections()
	current := draft

	for i := 0; i < max; i++ {
		if err := ctx.Err(); err != nil {
			return current, err
		}

		start := a.now()
		score, reason, evalErr := a.Config.Evaluator.Evaluate(ctx, in, current)
		if evalErr != nil {
			// 边界：评估失败必须降级为初稿，而不是让整次运行失败。
			current.AddStep(Step{
				Type:       StepObservation,
				Content:    "评估失败，返回初稿",
				Error:      evalErr.Error(),
				DurationMS: a.now().Sub(start).Milliseconds(),
			})
			a.warnf("reflexion evaluator failed: %v; returning the current draft", evalErr)
			return current, nil
		}
		current.AddStep(Step{
			Type:       StepThought,
			Content:    fmt.Sprintf("[评估] score=%.4g reason=%s", score, reason),
			DurationMS: a.now().Sub(start).Milliseconds(),
		})
		if score >= threshold {
			return current, nil
		}

		reflection, usage, calls, reflectErr := a.reflect(ctx, in, current, reason)
		if reflectErr != nil {
			a.warnf("reflexion reflection failed: %v; returning the current draft", reflectErr)
			return current, nil
		}
		current.Usage = addUsage(current.Usage, usage)
		current.LLMCalls += calls
		current.AddStep(Step{
			Type:    StepThought,
			Content: "[反思] " + reflection,
		})

		next := in
		// 反思文本追加到**下一次的 user 消息**里，不改写传入的历史切片。
		next.Query = composeReflexionQuery(in.Query, reflection)

		attempt, attemptErr := a.Base.Run(ctx, next)
		if attemptErr != nil {
			// 重试失败同样降级为当前草稿，而不是把整次运行判死。
			a.warnf("reflexion retry %d failed: %v; degrading to the previous draft", i+1, attemptErr)
			return current, nil
		}
		mergeInto(current, attempt)
		if budget := a.Config.MaxTokens; budget > 0 && current.Usage.TotalTokens >= budget {
			a.warnf("reflexion token budget reached (%d >= %d); stopping", current.Usage.TotalTokens, budget)
			return current, nil
		}
	}
	return current, nil
}

// reflect 生成反思文本。优先调用 LLM；失败或未配置时退化为模板文本。
//
// 返回的 usage/calls 用于把"反思本身"的消耗记进本次 Run，而不是记在 Agent 上。
func (a *ReflexionAgent) reflect(ctx context.Context, in Input, out *Output, reason string) (string, llm.Usage, int, error) {
	if a.LLM == nil {
		return renderReflection(reason), llm.Usage{}, 0, nil
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: a.reflectPrompt()},
		{Role: llm.RoleUser, Content: reflectRequest(in.Query, out.Text, reason)},
	}
	resp, err := a.LLM.Chat(ctx, &llm.ChatRequest{Messages: messages})
	if err != nil {
		a.warnf("reflexion llm failed: %v; using a template reflection", err)
		return renderReflection(reason), llm.Usage{}, 0, nil
	}
	if resp == nil {
		return renderReflection(reason), llm.Usage{}, 0, nil
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		text = renderReflection(reason)
	}
	return text, resp.Usage, 1, nil
}

func (a *ReflexionAgent) maxReflections() int {
	if a.Config.MaxReflections > 0 {
		return a.Config.MaxReflections
	}
	return DefaultMaxReflections
}

func (a *ReflexionAgent) threshold() float64 {
	if a.Config.Threshold > 0 {
		return a.Config.Threshold
	}
	return DefaultReflexionThreshold
}

func (a *ReflexionAgent) reflectPrompt() string {
	if p := strings.TrimSpace(a.Config.ReflectPrompt); p != "" {
		return p
	}
	return DefaultReflectPrompt
}

func (a *ReflexionAgent) warnf(format string, args ...any) {
	if a.Warn != nil {
		a.Warn(fmt.Sprintf(format, args...))
	}
}

func (a *ReflexionAgent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// composeReflexionQuery 把反思文本追加到下一次的 user 消息。
func composeReflexionQuery(query, reflection string) string {
	if reflection == "" {
		return query
	}
	return query + "\n\n【自我反思】\n" + reflection
}

// renderReflection 是不依赖 LLM 的反思模板（确定性，便于测试）。
func renderReflection(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "上一次作答未达到要求，请重新检查并给出更可靠的答案。"
	}
	return "上一次作答未达到要求：" + reason + "。请据此改进并重新作答。"
}

// reflectRequest 组装反思请求的用户消息。
func reflectRequest(query, draft, reason string) string {
	var b strings.Builder
	b.WriteString("原始任务：\n")
	b.WriteString(query)
	b.WriteString("\n\n未达标的作答：\n")
	b.WriteString(draft)
	b.WriteString("\n\n未达标原因：\n")
	b.WriteString(strings.TrimSpace(reason))
	return b.String()
}

// mergeInto 把后续尝试的 Steps / usage / 文本合并进累计输出。
func mergeInto(dst, src *Output) {
	if dst == nil || src == nil {
		return
	}
	dst.Steps = append(dst.Steps, src.Steps...)
	dst.ToolCalls = append(dst.ToolCalls, src.ToolCalls...)
	dst.Usage = addUsage(dst.Usage, src.Usage)
	dst.LLMCalls += src.LLMCalls
	if dst.PromptDigest == nil {
		dst.PromptDigest = src.PromptDigest
	}
	if src.MemoryDigest != "" {
		dst.MemoryDigest = src.MemoryDigest
	}
	dst.Text = src.Text
	dst.FinishReason = src.FinishReason
}

var _ Agent = (*ReflexionAgent)(nil)
var _ Evaluator = EvaluatorFunc(nil)
