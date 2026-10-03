package vector

import "testing"

// Test_F63_TextVectorIsDeterministic 钉住缓存的前提：同样的输入必须得到同样的向量。
func Test_F63_TextVectorIsDeterministic(t *testing.T) {
	t.Parallel()
	q := "你好，请问你是谁？"
	a := TextVector(q, TextDim)
	b := TextVector(q, TextDim)
	if len(a) != TextDim || len(b) != TextDim {
		t.Fatalf("维度不对: %d/%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("同一文本两次向量化不一致（第 %d 维）", i)
		}
	}
}

// Test_F63_TextBinaryExactMatchIsIdentical 覆盖"相同问题第二次直接命中"：
// 相同文本的二值编码必须逐位相同，汉明相似度为 1。
func Test_F63_TextBinaryExactMatchIsIdentical(t *testing.T) {
	t.Parallel()
	a := TextBinary("你是谁", TextDim)
	b := TextBinary("你是谁", TextDim)
	if Hamming(a, b) != 0 {
		t.Fatalf("相同文本的汉明距离应为 0，实际 %d", Hamming(a, b))
	}
	if got := hammingRatio(a, b); got != 1 {
		t.Fatalf("相似度应为 1，实际 %v", got)
	}
}

// Test_F63_TextBinaryPrefersOverlap 说明词面重合确实被编码进向量：
// 无关问题的相似度必须明显低于高度重合的问题。
func Test_F63_TextBinaryPrefersOverlap(t *testing.T) {
	t.Parallel()
	base := TextBinary("怎么配置机器人的回复策略", TextDim)
	near := TextBinary("怎么配置机器人的回复策略呢", TextDim)
	far := TextBinary("今天天气怎么样", TextDim)
	nearSim := hammingRatio(base, near)
	farSim := hammingRatio(base, far)
	if nearSim <= farSim {
		t.Fatalf("重合问题应更相似: near=%v far=%v", nearSim, farSim)
	}
}

func hammingRatio(a, b Binary) float64 {
	return 1 - float64(Hamming(a, b))/float64(a.Len())
}
