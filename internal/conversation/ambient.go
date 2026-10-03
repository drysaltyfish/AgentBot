package conversation

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/drysaltyfish/agentbot/internal/history"
)

// EstimateTokens 估算一段文本的 token 数。
//
// 用混合口径：CJK 一字约一 token，ASCII 四字符约一 token。
// 引入真正的分词器只为估算一个裁剪预算，不值得——而这个口径对中英混排足够接近，
// 且**纯计算、零依赖、可预测**。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, ascii := 0, 0
	for _, r := range s {
		if r < unicode.MaxASCII {
			ascii++
			continue
		}
		cjk++
	}
	// 向上取整：宁可高估一点，也不要因为低估而超预算。
	return cjk + (ascii+3)/4
}

// AmbientOptions 是环境消息的压缩参数。
type AmbientOptions struct {
	// TokenBudget 是环境消息允许占用的 token 预算；<=0 表示不压缩。
	TokenBudget int
	// MaxCharsPerMessage 是单条消息的字符上限，超长截断并提示可回溯；<=0 表示不截断。
	MaxCharsPerMessage int
}

// DefaultAmbientTokenBudget 是环境消息的默认预算。
//
// 取值思路：环境消息是**背景**，不该压过与机器人的实际对话；
// 但它又必须足够让模型看懂"刚才在聊什么"。
const DefaultAmbientTokenBudget = 1200

// DefaultAmbientMaxChars 是单条环境消息的默认字符上限。
const DefaultAmbientMaxChars = 200

// CompressAmbient 压缩**环境消息**（没被 @ 的那些）。
//
// 三条规则，按顺序应用：
//  1. 单条过长 -> 截断，并提示可以用 recall_history 看完整内容
//  2. 连续且内容完全相同 -> 合成一条并计数（多人刷屏只留一份）
//  3. 按 token 预算从**最近往前**保留，超出预算的更早消息整条丢弃
//
// 与"被 @ 的对话"分开处理是刻意的：环境消息按预算、对话按轮次。
// 混在一起的话，群里刷屏几分钟就能把真正的对话挤出窗口。
// 返回 (压缩后的条目, 从**前面**丢掉了多少条)。
// 返回丢弃条数是为了让调用方能按原始顺序把两类条目重新拼起来。
func CompressAmbient(items []history.Item, opts AmbientOptions) ([]history.Item, int) {
	if len(items) == 0 {
		return nil, 0
	}
	truncated := make([]history.Item, 0, len(items))
	for _, it := range items {
		truncated = append(truncated, truncateItem(it, opts.MaxCharsPerMessage))
	}

	merged := mergeAdjacentIdentical(truncated)
	if opts.TokenBudget <= 0 {
		return merged, 0
	}

	// 从最近往前累计，直到预算用尽。
	total := 0
	start := len(merged)
	for i := len(merged) - 1; i >= 0; i-- {
		cost := EstimateTokens(merged[i].RenderText())
		if total+cost > opts.TokenBudget && start != len(merged) {
			break
		}
		total += cost
		start = i
	}
	// 至少保留一条：预算再紧，也要让模型知道"刚才有人说过话"。
	if start == len(merged) && len(merged) > 0 {
		start = len(merged) - 1
	}
	// 丢弃条数按**源**条目算：合并只是渲染变化，不改变"哪些被保留"。
	dropped := 0
	if start < len(merged) && len(merged) > 0 {
		dropped = len(truncated) - len(merged) + start
	}
	return merged[start:], dropped
}

// truncateItem 截断过长内容，并留下回溯提示。
//
// 提示是必要的：只截断不说，模型会以为对方就说了这么点；
// 说清楚"被省略了、可以用 recall_history 看全"，它才知道该去取。
func truncateItem(it history.Item, maxChars int) history.Item {
	if maxChars <= 0 {
		return it
	}
	runes := []rune(it.Content)
	if len(runes) <= maxChars {
		return it
	}
	it.Content = string(runes[:maxChars]) +
		fmt.Sprintf("…（本条过长已省略 %d 字，可用 recall_history 查看完整内容）", len(runes)-maxChars)
	return it
}

// mergeAdjacentIdentical 把**连续且内容完全相同**的消息合成一条并计数。
//
// 口径刻意保守（完全相同、且必须连续）：
// 相似度合并会吃掉不同的话，而误合并是看不见的信息丢失。
func mergeAdjacentIdentical(items []history.Item) []history.Item {
	if len(items) <= 1 {
		return items
	}
	out := make([]history.Item, 0, len(items))
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && sameAmbientContent(items[i], items[j]) {
			j++
		}
		if j-i == 1 {
			out = append(out, items[i])
			i = j
			continue
		}
		// 合并标签必须区分「同一人重复」与「多个人各发一次」——
		// 实测踩过：一个人连发 10 条相同表情，标签写成「10人重复」会让模型
		// 以为有十个人在刷屏，把事实搞反（而它恰好要回答"我发了几次"）。
		speakers := map[int64]struct{}{}
		for k := i; k < j; k++ {
			speakers[items[k].SpeakerID] = struct{}{}
		}
		merged := items[i]
		merged.SpeakerID = 0
		switch {
		case len(speakers) <= 1:
			merged.SpeakerName = fmt.Sprintf("同一人重复%d次", j-i)
		case len(speakers) == j-i:
			merged.SpeakerName = fmt.Sprintf("%d人各发一次", j-i)
		default:
			merged.SpeakerName = fmt.Sprintf("%d人共发%d次", len(speakers), j-i)
		}
		merged.Content = strings.TrimSpace(items[i].Content)
		out = append(out, merged)
		i = j
	}
	return out
}

// sameAmbientContent 判断两条环境消息是否算"同一句"。
//
// 只比正文：不同的人说同样的话正是要合并的情况。
func sameAmbientContent(a, b history.Item) bool {
	if a.Kind != history.KindUser || b.Kind != history.KindUser {
		return false
	}
	return strings.TrimSpace(a.Content) == strings.TrimSpace(b.Content) && a.Content != ""
}
