package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// 前缀关系取值。
const (
	// RelationIdentical 表示与上一轮逐条相同。
	RelationIdentical = "identical"
	// RelationExtended 表示上一轮是本轮的前缀——**追加式增长，缓存最优**。
	RelationExtended = "extended"
	// RelationSlid 表示窗口向前滑动（前几条被裁掉），属**预期**变化。
	RelationSlid = "slid"
	// RelationDiverged 表示既非追加也非滑动——**意料之外的前缀变化**。
	RelationDiverged = "diverged"
)

// PrefixRelation 描述相邻两次请求的消息序列之间的关系（F-89）。
type PrefixRelation struct {
	Relation string
	// CommonPrefix 是两条序列从头开始相同的条数。
	CommonPrefix int
	// SlidBy 仅在 Relation 为 slid 时有意义：上一轮开头被裁掉了几条。
	SlidBy int
	// PrevLen / NextLen 便于日志里直接看出规模变化。
	PrevLen int
	NextLen int
}

// Unexpected 表示这次前缀变化不该发生（不是追加，也不是窗口滑动）。
//
// 这是本特性真正的价值：把"前缀为什么变了"从**事后猜**变成**当场知道**。
func (r PrefixRelation) Unexpected() bool { return r.Relation == RelationDiverged }

// Digest 返回消息序列的逐条指纹。
//
// 只存指纹不存正文：正文可能含隐私，而前缀比较只需要判断"这一段是否逐字节相同"。
// 每条独立取指纹，因此可以直接比较到**分歧点在哪一条**，而不是只知道"整体不同"。
func Digest(messages []Message) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		h := sha256.New()
		_, _ = h.Write([]byte(m.Role))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(m.Content))
		_, _ = h.Write([]byte{0})
		// 工具调用与工具结果也必须参与指纹：只比 role+content 会把
		// "两个不同的工具调用"看成一模一样。
		for _, tc := range m.ToolCalls {
			_, _ = h.Write([]byte(tc.ID))
			_, _ = h.Write([]byte(tc.Name))
			_, _ = h.Write([]byte(tc.Arguments))
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(m.ToolCallID))
		_, _ = h.Write([]byte{0})
		out = append(out, hex.EncodeToString(h.Sum(nil)[:12]))
	}
	return out
}

// ComparePrefix 判定上一轮与本轮的关系。
//
// 顺序很重要：
//  1. 完全相同
//  2. 上一轮是本轮的前缀（追加式——理想情况）
//  3. 上一轮裁掉开头若干条后是本轮的前缀（窗口滑动——预期）
//  4. 都不满足：**分歧**，说明前缀被改写了
func ComparePrefix(prev, next []string) PrefixRelation {
	rel := PrefixRelation{PrevLen: len(prev), NextLen: len(next)}
	rel.CommonPrefix = commonPrefixLen(prev, next)

	if len(prev) == len(next) && rel.CommonPrefix == len(prev) {
		rel.Relation = RelationIdentical
		return rel
	}
	if rel.CommonPrefix == len(prev) {
		rel.Relation = RelationExtended
		return rel
	}
	// 滑动：试着把上一轮的开头逐条裁掉，看能否成为本轮的前缀。
	// 上限取 len(prev)，但通常只会裁掉前几条。
	for k := 1; k < len(prev); k++ {
		if isPrefix(prev[k:], next) {
			rel.Relation = RelationSlid
			rel.SlidBy = k
			return rel
		}
	}
	rel.Relation = RelationDiverged
	return rel
}

func commonPrefixLen(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func isPrefix(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	return commonPrefixLen(prefix, full) == len(prefix)
}

// EncodeDigest 把指纹列表编码成一列字符串（存库用）。
func EncodeDigest(d []string) string { return strings.Join(d, ",") }

// DecodeDigest 解码指纹列表。
func DecodeDigest(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
