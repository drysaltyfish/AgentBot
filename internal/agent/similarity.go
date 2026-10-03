package agent

import "strings"

// SimilarityThreshold 是判定"同一条记忆"的字符二元组 Jaccard 相似度阈值。
//
// F-48 要求"相同内容重复保存时做去重（相似度 > 阈值则更新而非新增）"。
// 纯精确比较不够用：同一件事被不同措辞写出来很常见，实测就出现过
// 「我喜欢喝橙汁」与「用户喜欢喝橙汁」并存——它们是同一条记忆。
const SimilarityThreshold = 0.5

// bigrams 返回字符串的字符二元组集合（对中文比按词切更稳，且无需分词器）。
func bigrams(s string) map[string]struct{} {
	runes := []rune(strings.TrimSpace(s))
	out := make(map[string]struct{}, len(runes))
	if len(runes) == 1 {
		out[string(runes)] = struct{}{}
		return out
	}
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = struct{}{}
	}
	return out
}

// Similarity 返回两条记忆的字符二元组 Jaccard 相似度（0~1）。
func Similarity(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	inter := 0
	for g := range ga {
		if _, ok := gb[g]; ok {
			inter++
		}
	}
	union := len(ga) + len(gb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// IsDuplicateMemory 判断新事实是否与某条既有记忆重复。
func IsDuplicateMemory(newer, existing string) bool {
	if strings.TrimSpace(newer) == strings.TrimSpace(existing) {
		return true
	}
	return Similarity(newer, existing) >= SimilarityThreshold
}
