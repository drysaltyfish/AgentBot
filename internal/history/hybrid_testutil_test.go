package history

import "github.com/drysaltyfish/agentbot/internal/retrieval"

// HybridConfigForTest 返回一份确定性的融合参数：两路都开、TopK=5。
//
// 测试不直接用零值 HybridConfig，是因为"零值也有默认"这条约定属于
// internal/memory 的单元测试；这里测的是接线，参数应当显式。
func HybridConfigForTest() retrieval.HybridConfig {
	return retrieval.HybridConfig{TopK: 5, CandidateK: 10}
}
