package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

// reflexionEvalPrompt 是 F-36 的评估提示词。
//
// 要求"一行、分数在前"，是因为评估结果要能被确定性解析：
// 解析不出来时我们宁可报错让 Reflexion 降级为初稿，也不猜一个分数——
// 猜错的分数会让系统去做无意义的重试。
const reflexionEvalPrompt = "你是回答质量评估器。给下面这次回答打分：0 到 1 之间的小数，" +
	"越接近 1 越好。同一行给出不超过 20 字的理由。只输出这一行，格式：<分数> <理由>"

// llmEvaluator 用模型给草稿打分，实现 agent.Evaluator（F-36）。
type llmEvaluator struct{ model llm.LLM }

// Evaluate 实现 agent.Evaluator。
func (e llmEvaluator) Evaluate(ctx context.Context, in agent.Input, out *agent.Output) (float64, string, error) {
	if e.model == nil {
		return 0, "", fmt.Errorf("reflexion: 没有可用的模型")
	}
	if out == nil {
		return 0, "", fmt.Errorf("reflexion: 草稿为空")
	}
	resp, err := e.model.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: reflexionEvalPrompt},
			{Role: llm.RoleUser, Content: "【问题】\n" + in.Query + "\n\n【回答】\n" + out.Text},
		},
		Temperature: 0,
		MaxTokens:   64,
	})
	if err != nil {
		return 0, "", fmt.Errorf("reflexion evaluate: %w", err)
	}
	if resp == nil {
		return 0, "", fmt.Errorf("reflexion: 评估调用没有返回内容")
	}
	return parseEvalLine(resp.Content)
}

// parseEvalLine 解析"<分数> <理由>"。
func parseEvalLine(s string) (float64, string, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("reflexion: 评估输出为空")
	}
	score, err := strconv.ParseFloat(strings.Trim(fields[0], "：:"), 64)
	if err != nil {
		return 0, "", fmt.Errorf("reflexion: 无法解析分数 %q: %w", fields[0], err)
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score, strings.Join(fields[1:], " "), nil
}

// orchestratorWorker 是编排范式下默认 worker 的名字。
const orchestratorWorker = "default"

// wrapParadigm 按配置把基础 Agent 包成 Reflexion / Orchestrator（F-36 / F-37）。
//
// 未配置或取值不认识时**原样返回基础实现**：范式是可选增强，
// 配置写错不该让机器人整体不可用（配置校验负责提示拼写问题）。
func wrapParadigm(cfg *config.Config, model llm.LLM, base agent.Agent, lg *observe.Logger) agent.Agent {
	switch cfg.Agent.EffectiveParadigm() {
	case "reflexion":
		lg.Component("agent").Info("reflexion paradigm enabled",
			"max_reflections", cfg.Agent.EffectiveMaxReflections(),
			"threshold", cfg.Agent.EffectiveThreshold())
		return &agent.ReflexionAgent{
			Base: base,
			LLM:  model,
			Config: agent.ReflexionConfig{
				MaxReflections: cfg.Agent.EffectiveMaxReflections(),
				Threshold:      cfg.Agent.EffectiveThreshold(),
				Evaluator:      llmEvaluator{model: model},
			},
			Warn: func(msg string) { lg.Component("agent").Warn(msg) },
		}
	case "orchestrator":
		lg.Component("agent").Info("orchestrator paradigm enabled", "worker", orchestratorWorker)
		return &agent.Orchestrator{
			LLM:           model,
			Workers:       map[string]agent.Worker{orchestratorWorker: agent.NewWorker(orchestratorWorker, base)},
			DefaultWorker: orchestratorWorker,
			Warn:          func(msg string) { lg.Component("agent").Warn(msg) },
		}
	default:
		return base
	}
}
