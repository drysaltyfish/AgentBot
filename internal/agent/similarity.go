package agent

import "github.com/drysaltyfish/agentbot/internal/textsim"

// 相似度实现下沉到 internal/textsim：存储层也要用同一套判据，
// 而存储层不能反向依赖 agent。这里保留同名入口，避免调用方四处改。

// SimilarityThreshold 是判定"同一条记忆"的相似度阈值。
const SimilarityThreshold = textsim.Threshold

// Similarity 返回两条记忆的字符二元组 Jaccard 相似度。
func Similarity(a, b string) float64 { return textsim.Similarity(a, b) }

// IsDuplicateMemory 判断新事实是否与某条既有记忆重复。
func IsDuplicateMemory(newer, existing string) bool { return textsim.IsDuplicate(newer, existing) }
