package llm

// Price 是模型单价（每百万 token 的美元价）。
//
// **版本号是必需的**：供应商改价后，历史数据必须仍能按当时的价格解释。
// 只存一个金额而不存"按什么价算的"，过一段时间就没人说得清那些数字是怎么来的。
type Price struct {
	Version            string
	InputPerMillion    float64
	OutputPerMillion   float64
	CacheHitPerMillion float64
}

// Enabled 表示是否配置了可用的价格（全 0 视为未配置）。
func (p Price) Enabled() bool {
	return p.InputPerMillion > 0 || p.OutputPerMillion > 0 || p.CacheHitPerMillion > 0
}

// Cost 按本价格计算一次用量的估计成本。
//
// 命中与未命中**分开计价**：供应商对缓存命中通常给大幅折扣，
// 把两者合起来按 input 计价会把成本算高，从而让"缓存优化值不值"这个判断失真。
//
// 注意供应商上报的 PromptTokens 含命中部分，因此这里只用命中/未命中两列，
// 不额外叠加 PromptTokens，避免重复计费。
func (p Price) Cost(u Usage) float64 {
	const perMillion = 1_000_000.0
	return float64(u.PromptCacheHitTokens)/perMillion*p.CacheHitPerMillion +
		float64(u.PromptCacheMissTokens)/perMillion*p.InputPerMillion +
		float64(u.CompletionTokens)/perMillion*p.OutputPerMillion
}
