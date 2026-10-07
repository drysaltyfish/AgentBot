package textsim

import (
	"strings"
	"unicode"
)

// Tokenize 把文本切成检索用 token：
//
//   - ASCII/其它字母数字连续段整体小写后作为一个 token（命令、ID、英文词）；
//   - 中日韩统一表意文字连续段切成"单字 + 相邻二字组"（中文无需分词即可召回）。
//
// 同时保留单字与二字组：单字让"猫"这类极短查询也能命中，
// 二字组提供"北京烤鸭"这类专有名词的区分度；常见字由 BM25 的 IDF 自然降权。
//
// 放在 textsim 而不是某个检索实现里：混合检索与分层记忆的排序都要用它，
// 而两边谁都不该依赖对方（那正是拆分之前的形状）。
func Tokenize(s string) []string {
	var out []string
	var word []rune
	var han []rune

	flushWord := func() {
		if len(word) == 0 {
			return
		}
		out = append(out, strings.ToLower(string(word)))
		word = word[:0]
	}
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		for _, r := range han {
			out = append(out, string(r))
		}
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

// UniqueTokens 返回去重后的 token；保持首次出现顺序，保证确定性。
func UniqueTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// LexicalScore 返回 query 的 token 在 text 中命中的比例（0~1）。
//
// 确定性的词面重叠打分，用于排序兜底：混合检索的 BM25 一路与分层记忆的
// 召回排序都需要它，且必须用同一套切词口径，否则同一句话在两条路径上得分不同。
func LexicalScore(query, text string) float64 {
	qt := UniqueTokens(Tokenize(query))
	if len(qt) == 0 {
		return 0
	}
	set := make(map[string]struct{})
	for _, t := range Tokenize(text) {
		set[t] = struct{}{}
	}
	hit := 0
	for _, t := range qt {
		if _, ok := set[t]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(qt))
}
