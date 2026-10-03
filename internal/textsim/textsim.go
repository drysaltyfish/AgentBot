// Package textsim 提供与领域无关的文本相似度计算。
//
// 独立成包是因为**存储层与 Agent 层都要用它**：存储层要在写入记忆时判定"是不是同一条"，
// Agent 层要在内存实现里做同样的事。放在任何一方都会造成反向依赖。
package textsim

import (
	"strings"
	"unicode/utf8"
)

// Threshold 是判定"同一件事"的字符二元组 Jaccard 相似度阈值。
//
// 取 0.5 的依据是实测的一对真实文本：
//
//	"我喜欢喝橙汁" vs "用户喜欢喝橙汁" ≈ 0.57  -> 判为同一条
//	"我喜欢喝橙汁" vs "我喜欢喝冰美式" ≈ 0.43  -> 不合并
//
// **误合并的代价高于漏合并**：合并会静默丢掉一条不同的记忆，
// 而漏合并只是多一条重复——后者用户看得见，前者看不见。
const Threshold = 0.5

// Similarity 返回两条文本的字符二元组 Jaccard 相似度（0~1）。
//
// 用字符二元组而非分词：中文分词要引依赖，而短事实场景下二元组已经够稳。
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

// MinRunesForSimilarity 是用相似度做去重判定所需的最短长度。
//
// 短文本的字符二元组太少，Jaccard 噪声极大：4 个字的句子只有 3 个二元组，
// 共用 2 个就到了 0.50。实测「旧的一条」与「新的一条」正好落在 0.50 而被误合并——
// 而它们是两件不同的事。低于这个长度时只认**完全相同**。
const MinRunesForSimilarity = 6

// IsDuplicate 判断两条文本是否算同一条。
//
// 这是**保守**的确定性判据：宁可漏合并（多一条重复，用户看得见），
// 也不误合并（静默少一条记忆，用户看不见）。真正的语义歧义交给上层判官。
func IsDuplicate(a, b string) bool {
	ta, tb := strings.TrimSpace(a), strings.TrimSpace(b)
	if ta == tb {
		return true
	}
	if utf8.RuneCountInString(ta) < MinRunesForSimilarity ||
		utf8.RuneCountInString(tb) < MinRunesForSimilarity {
		return false
	}
	return Similarity(ta, tb) >= Threshold
}

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
