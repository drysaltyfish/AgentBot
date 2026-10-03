// Package conversation 按"缓存优先"原则装配模型请求（对齐 DeepSeek 前缀缓存）。
//
// DeepSeek 的上下文硬盘缓存只有在"完整匹配缓存前缀单元"时才命中
// （见 api-docs.deepseek.com/zh-cn/guides/kv_cache）。据此把上下文切成三段：
//
//	IMMUTABLE PREFIX   system：整个会话逐字节不变，是缓存命中的起点
//	APPEND-ONLY LOG    历史：只追加，绝不重排、绝不改写
//	VOLATILE SCRATCH   当前输入：唯一允许每轮变化的部分
//
// 违反任何一条都会让命中率塌陷。"每轮重排、改写、或注入新时间戳"是常见错误，
// 也是本包存在的原因——把不变量收敛到一处，并用测试守住。
//
// 实测（deepseek-flash，2026-10）：
//   - 前缀约 1658 token：首次 0%，6 秒后完全相同的请求 84.9%，追加一轮 91.9%
//   - 前缀 53~77 token：反复请求仍恒为 0%——短前缀进不了缓存
//   - 思考模式开关对命中率无影响
//
// 结论：前缀越长收益越大，因此稳定的 system 与只追加的历史才是缓存资产；
// 同时低频裁剪（见 history.HighWater）比每轮裁剪重要得多。
package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

// DefaultSystemPrompt 是默认的不可变前缀。
//
// 严禁在这里放时间戳、随机数、请求计数器等易变内容：那会让前缀逐字节变化，
// 缓存永远无法命中。需要时间等易变信息时，放进当前这条用户消息里。
const DefaultSystemPrompt = "你是 AgentBot，一个运行在聊天平台上的助手。" +
	"回答要简洁、直接、口语化，适合在聊天窗口中阅读。" +
	"不确定的信息要说明不确定，不要编造。"

// Options 配置 Assembler。
type Options struct {
	// System 是不可变前缀正文；为空时使用 DefaultSystemPrompt。
	System string
	// MaxHistory 是最多回灌的历史条数；<=0 表示不限制。
	//
	// 注意：如果调用方已有存储层裁剪，这里应保持 0。在本层逐轮裁剪会让前缀逐轮
	// 变化，从而让前缀缓存彻底失效——实测命中率会从 ~90% 掉到 0%。
	MaxHistory int
}

// Assembler 把三段装配成消息序列。
type Assembler struct {
	prefix string
	hash   string
	max    int
}

// New 构造 Assembler；前缀在此固定，之后不再改变。
func New(opts Options) *Assembler {
	system := opts.System
	if system == "" {
		system = DefaultSystemPrompt
	}
	return &Assembler{prefix: system, hash: shortHash(system), max: opts.MaxHistory}
}

// Prefix 返回不可变前缀正文。
func (a *Assembler) Prefix() string { return a.prefix }

// PrefixHash 返回不可变前缀的短摘要，用于在日志里证明前缀跨轮未变。
func (a *Assembler) PrefixHash() string { return a.hash }

// Build 装配 [不可变前缀] + [只追加历史] + [当前输入]。
//
// 顺序即不变量：前缀永远在最先，当前输入永远在最后，中间只有按原顺序追加的历史。
func (a *Assembler) Build(hist []history.Item, user string) []llm.Message {
	items := trimHistory(hist, a.max)
	out := make([]llm.Message, 0, len(items)+2)
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: a.prefix})
	out = append(out, ToMessages(items)...)
	out = append(out, llm.Message{Role: llm.RoleUser, Content: user})
	return out
}

// ToMessages 把历史条目转成消息序列。
//
// 导出是因为走 ReAct 时组合根也要做同样的映射——映射规则必须只有一份，
// 否则两条路径的历史形状会悄悄漂移，而历史形状又直接影响前缀缓存。
func ToMessages(items []history.Item) []llm.Message {
	out := make([]llm.Message, 0, len(items))
	for _, it := range items {
		if m, ok := toMessage(it); ok {
			out = append(out, m)
		}
	}
	return out
}

// toMessage 把历史条目转成消息；marker 是内部草稿，永远不上行。
func toMessage(it history.Item) (llm.Message, bool) {
	switch it.Kind {
	case history.KindUser:
		return llm.Message{Role: llm.RoleUser, Content: it.Content}, true
	case history.KindAssistant:
		m := llm.Message{Role: llm.RoleAssistant, Content: it.Content, Name: it.Name}
		for _, tc := range it.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		return m, true
	case history.KindToolCall:
		var calls []llm.ToolCall
		for _, tc := range it.ToolCalls {
			calls = append(calls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}, true
	case history.KindToolResult:
		return llm.Message{Role: llm.RoleTool, Content: it.Content, ToolCallID: it.ToolCallID}, true
	case history.KindMarker:
		// VOLATILE SCRATCH 的落点：内部标记只用于本地状态，绝不上行。
		return llm.Message{}, false
	default:
		return llm.Message{}, false
	}
}

// trimHistory 返回要呈现给模型的历史窗口。
//
// 与"每轮取最近 N 条"不同：窗口起点按 margin（max 的一半）对齐，因此每
// margin/2 轮才向前移动一次。窗口在两次移动之间**完全稳定**——逐轮滑动的窗口
// 会让请求前缀每轮都变，前缀缓存必然失效。
//
// 这样存储层可以保留远多于窗口的历史（见 config.History.Retention），
// recall_history 才有"窗口之外"的东西可召回；否则那个工具只能返回
// 已经出现在提示词里的内容，等于摆设。
func trimHistory(items []history.Item, max int) []history.Item {
	if max <= 0 || len(items) <= max {
		return items
	}
	margin := max / 2
	if margin < 1 {
		margin = 1
	}
	start := ((len(items) - max) / margin) * margin
	// 尽量对齐到 user 轮边界（从 assistant/tool 中间开始会让模型看到孤立的回答），
	// 但最多向前找 margin 条，避免窗口被压得过小。
	for i := 0; i < margin && start < len(items) && items[start].Kind != history.KindUser; i++ {
		start++
	}
	if start >= len(items) {
		start = len(items) - 1
	}
	return items[start:]
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// Fingerprint 返回一组消息的"前缀指纹"，用于测试断言前缀稳定性。
func Fingerprint(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteByte(0x1f)
		b.WriteString(m.Content)
		b.WriteByte(0x1e)
	}
	return shortHash(b.String())
}
