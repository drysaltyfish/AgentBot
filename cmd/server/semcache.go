package main

import (
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/semcache"
	"github.com/drysaltyfish/agentbot/internal/vector"
)

// buildSemcache 按配置构造语义缓存（F-63）。未启用时返回 (nil, nil)。
//
// 向量化用 F-63 明确允许的"二值哈希"分支：常见问题重复出现这个场景靠词面
// 重合已经够用，引入 embedding 会让一个纯缓存特性依赖网络与配额。
//
// Filter 接出口过滤链（F-55）：缓存里存的必须是"实际会发出去的那版文本"。
// 发送侧虽然还会再过滤一遍，但存进缓存的若是未过滤原文，命中时就已经绕过了
// 一道防线。
func buildSemcache(cfg *config.Config, chain *outbound.Chain, lg *observe.Logger) (*semcache.Cache, error) {
	if !cfg.Semcache.EffectiveEnabled() {
		return nil, nil
	}
	filter := func(answer string) (string, bool) { return answer, true }
	if chain != nil {
		filter = func(answer string) (string, bool) { return chain.Apply(answer), true }
	}
	c, err := semcache.New(semcache.Options{
		Vectorize:    func(q string) vector.Binary { return vector.TextBinary(q, vector.TextDim) },
		Threshold:    cfg.Semcache.EffectiveThreshold(),
		TTL:          cfg.Semcache.EffectiveTTL(),
		MaxEntries:   cfg.Semcache.EffectiveMaxEntries(),
		SkipList:     cfg.Semcache.SkipWords,
		SkipPatterns: cfg.Semcache.SkipPatterns,
		Filter:       filter,
		TokenCount: func(answer string) int {
			n := len([]rune(answer))
			if n == 0 {
				return 0
			}
			return (n + 3) / 4
		},
		Warn: func(msg string) { lg.Component("semcache").Warn(msg) },
	})
	if err != nil {
		return nil, err
	}
	lg.Component("semcache").Info("semantic cache is enabled",
		"threshold", cfg.Semcache.EffectiveThreshold(),
		"ttl", cfg.Semcache.EffectiveTTL().String(),
		"max_entries", cfg.Semcache.EffectiveMaxEntries())
	return c, nil
}
