package vector

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// TextDim 是文本特征哈希向量的默认维度（64 维 = 8 字节，与 F-50 的默认一致）。
const TextDim = 64

// TextVector 把文本映射成确定性的特征哈希向量（F-63 的"二值哈希"分支）。
//
// 为什么不用 embedding：F-63 明确允许二值哈希，而"常见问题重复出现"这个场景
// 靠词面重合已经够用；引入模型会让一个纯缓存特性依赖网络与配额。
//
// 分词与记忆检索同一套直觉：ASCII/数字连续段整体小写作为一个 token，
// 汉字切成单字 + 相邻二字组。每个 token 用 FNV-1a 落到某一维，按符号累加，
// 最后做 L2 归一化。
//
// 确定性是硬要求：同样的输入必须得到同样的向量，否则缓存永远不命中。
func TextVector(text string, dim int) []float32 {
	if dim <= 0 {
		dim = TextDim
	}
	v := make([]float32, dim)
	for _, tok := range TextTokens(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		sum := h.Sum32()
		idx := int(sum % uint32(dim)) //nolint:gosec // dim 为正且远小于 uint32 上限
		// 用最高位决定符号：只按桶计数会让"多了个词"和"少了个词"看起来一样。
		if sum&0x80000000 != 0 {
			v[idx]--
		} else {
			v[idx]++
		}
	}
	if n := math.Sqrt(Norm(v)); n > 0 {
		for i := range v {
			v[i] /= float32(n)
		}
	}
	return v
}

// TextBinary 返回文本的二值编码（F-63 的 Vectorize 直接用它）。
func TextBinary(text string, dim int) Binary { return Encode(TextVector(text, dim)) }

// TextTokens 切分检索用 token：ASCII 词整体、汉字单字与相邻二字组。
func TextTokens(s string) []string {
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
