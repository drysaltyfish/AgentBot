package main

import (
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

// budgetWiring 把"上下文预算"和它的实测计数器绑在一起。
//
// 为什么要绑成一个值：F-32 规定 token 计数的优先级是
// **provider 返回的真实 usage > 本地 tokenizer > 启发式估算**。
// 这条只能由两处协作完成——
//
//	预算侧：用计数器算"这份消息序列占多少 token"，据此裁剪；
//	观察侧：每次真实响应回来后，把 provider 报的 prompt_tokens 喂回去。
//
// 两者必须是**同一个**计数器。各建一个的后果是静默的：喂进去的实测值
// 永远命不中预算那边的缓存键，于是"接上了"和"没接"表现得一模一样——
// 预算永远停在最低那一档的启发式估算上。
type budgetWiring struct {
	// Budget 为 nil 表示未启用预算（未配置 max_context）。
	Budget *llm.Budget
	// Counter 是可选的实测计数器；Budget 为 nil 时它也是 nil。
	Counter *llm.MeasuredCounter
}

// buildBudget 按配置构造上下文预算（F-32）。
//
// 未配置 max_context 时返回零值：预算是**保护性**开关，默认不改变任何请求字节。
// 开启后每次请求按"窗口 - 输出预留 - 工具预留"裁剪，工具 schema 的实际占用
// 超过预留时按实际值预留（FitRequest 内部处理）。
//
// 计数器只在预算启用时创建：没有裁剪需求时记录实测值没有意义，
// 只会白占一张随前缀增长的表。
func buildBudget(cfg *config.Config, lg *observe.Logger) budgetWiring {
	if cfg == nil {
		return budgetWiring{}
	}
	maxContext := cfg.LLM.EffectiveMaxContext()
	if maxContext <= 0 {
		return budgetWiring{}
	}
	// 启发式兜底 + 实测优先，即 F-32 要求的顺序。
	counter := llm.NewMeasuredCounter(llm.HeuristicCounter{})
	return budgetWiring{
		Budget: &llm.Budget{
			MaxContext:    maxContext,
			ReserveOutput: cfg.LLM.EffectiveReserveOutput(),
			ReserveTools:  cfg.LLM.EffectiveReserveTools(),
			Model:         cfg.LLM.Model,
			Counter:       counter,
			OnError: func(err error) {
				if lg != nil {
					lg.Component("llm").Warn("context budget summarizer failed; falling back to plain trimming", "error", err)
				}
			},
		},
		Counter: counter,
	}
}
