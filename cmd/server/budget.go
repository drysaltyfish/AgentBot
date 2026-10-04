package main

import (
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

// buildBudget 按配置构造上下文预算（F-32）。
//
// 未配置 max_context 时返回 nil：预算是**保护性**开关，默认不改变任何请求字节。
// 开启后每次请求按"窗口 - 输出预留 - 工具预留"裁剪，工具 schema 的实际占用
// 超过预留时按实际值预留（FitRequest 内部处理）。
func buildBudget(cfg *config.Config, lg *observe.Logger) *llm.Budget {
	if cfg == nil {
		return nil
	}
	maxContext := cfg.LLM.EffectiveMaxContext()
	if maxContext <= 0 {
		return nil
	}
	return &llm.Budget{
		MaxContext:    maxContext,
		ReserveOutput: cfg.LLM.EffectiveReserveOutput(),
		ReserveTools:  cfg.LLM.EffectiveReserveTools(),
		Model:         cfg.LLM.Model,
		OnError: func(err error) {
			if lg != nil {
				lg.Component("llm").Warn("context budget summarizer failed; falling back to plain trimming", "error", err)
			}
		},
	}
}
