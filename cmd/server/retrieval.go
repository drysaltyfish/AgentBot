package main

import (
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

// wrapHistoryWithRetrieval 按配置决定是否给历史存储套上混合检索（F-51）。
//
// 返回的仍是 history.History（Hybrid 嵌入原实现，全部方法透传），因此调用方
// 不需要知道检索是否启用；recall_history 通过类型断言发现 Searcher 后自行改走检索。
func wrapHistoryWithRetrieval(cfg *config.Config, base history.History, lg *observe.Logger) history.History {
	if cfg == nil || base == nil || !cfg.Retrieval.EffectiveEnabled() {
		return base
	}
	hybrid := history.NewHybrid(base, memory.HybridConfig{
		KeywordWeight: cfg.Retrieval.EffectiveKeywordWeight(),
		VectorWeight:  cfg.Retrieval.EffectiveVectorWeight(),
		TopK:          cfg.Retrieval.EffectiveTopK(),
		CandidateK:    cfg.Retrieval.EffectiveCandidateK(),
	})
	if lg != nil {
		lg.Component("memory").Info("hybrid retrieval is enabled for history recall",
			"keyword_weight", cfg.Retrieval.EffectiveKeywordWeight(),
			"vector_weight", cfg.Retrieval.EffectiveVectorWeight(),
			"top_k", cfg.Retrieval.EffectiveTopK())
	}
	return hybrid
}
