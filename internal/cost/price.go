package cost

import (
	"errors"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// ErrUnknownModel 表示价格表未配置该模型且未知模型策略为拒绝。
var ErrUnknownModel = errors.New("cost: unknown model")

// ErrInvalidUsage 表示 token 数为负等非法用量。
var ErrInvalidUsage = errors.New("cost: invalid usage")

// ErrClosed 表示 Tracker 已关闭，不再接受记录。
var ErrClosed = errors.New("cost: tracker closed")

// Price 是模型每 1K token 的美元单价（FEATURES.md F-66 的价格表口径）。
type Price struct {
	// InputPer1K 是每 1K prompt token 的美元价。
	InputPer1K float64
	// OutputPer1K 是每 1K completion token 的美元价。
	OutputPer1K float64
}

// Cost 按本价格计算一次费用。
//
// 复用 llm.Price 的计价实现：每百万单价 = 每 1K 单价 × 1000；prompt token
// 落入未命中列、completion token 落入输出列，避免与缓存列重复计费。
func (p Price) Cost(promptTokens, completionTokens int) float64 {
	lp := llm.Price{
		InputPerMillion:  p.InputPer1K * 1000,
		OutputPerMillion: p.OutputPer1K * 1000,
	}
	return lp.Cost(llm.Usage{
		PromptCacheMissTokens: promptTokens,
		CompletionTokens:      completionTokens,
	})
}

// UnknownPolicy 决定价格表中不存在的模型如何计价。
type UnknownPolicy int

const (
	// UnknownWarnZero 按 0 计费并触发一次告警（FEATURES.md F-66 默认策略）。
	// “显式告警 + 0 计费”区别于静默按 0，调用方必须能看见未配置的模型。
	UnknownWarnZero UnknownPolicy = iota
	// UnknownReject 拒绝该次记录，Cost 返回 ErrUnknownModel。
	UnknownReject
)

// PriceTable 是模型到单价的只读映射。
type PriceTable struct {
	// Prices 以模型名为键；构造后不得再修改。
	Prices map[string]Price
	// Unknown 是未知模型的策略；零值为 UnknownWarnZero。
	Unknown UnknownPolicy
}

// Lookup 查找模型单价；ok 为 false 表示未配置。
func (t PriceTable) Lookup(model string) (Price, bool) {
	p, ok := t.Prices[model]
	return p, ok
}

// Cost 计算一次费用。known 为 false 表示模型未配置；此时按 Unknown 策略
// 返回 0（UnknownWarnZero）或 ErrUnknownModel（UnknownReject）。
func (t PriceTable) Cost(model string, promptTokens, completionTokens int) (amount float64, known bool, err error) {
	p, ok := t.Lookup(model)
	if ok {
		return p.Cost(promptTokens, completionTokens), true, nil
	}
	if t.Unknown == UnknownReject {
		return 0, false, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	return 0, false, nil
}
