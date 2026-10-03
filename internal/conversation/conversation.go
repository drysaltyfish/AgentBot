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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
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
	// AmbientTokenBudget 是环境消息的 token 预算；0 用默认值，负数表示不压缩。
	AmbientTokenBudget int
	// AmbientMaxChars 是单条环境消息的字符上限；0 用默认值。
	AmbientMaxChars int
	// HalfStatic 按会话返回"半静态段"正文（F-65：人格设定，小时级变化）。
	//
	// 为空表示没有该段。返回值必须逐字节稳定在 (会话, 人格) 上：同一人格的
	// 不同会话得到同一段文本，跨会话共享前缀缓存的收益才不被破坏；因此
	// **不要把 RouteKey/时间戳等每会话不同的内容写进正文**，它们只进日志与指标。
	HalfStatic func(ctx context.Context, key session.Key) string
}

// Assembler 把三段装配成消息序列。
//
// 三段按变化频率排列（F-65）：静态段（进程启动期内逐字节稳定）→ 半静态段
// （人格设定，小时级）→ 动态段（时间/最近对话/当前输入，每轮都变）。
// 物理布局见 ADR-0002：半静态段的"人格设定"并入 system 消息头，
// "长期记忆"作为紧随其后的独立消息——两者同属半静态频率类，但位置不同。
type Assembler struct {
	prefix     string
	hash       string
	max        int
	ambient    AmbientOptions
	halfStatic func(ctx context.Context, key session.Key) string
}

// New 构造 Assembler；前缀在此固定，之后不再改变。
func New(opts Options) *Assembler {
	system := opts.System
	if system == "" {
		system = DefaultSystemPrompt
	}
	ambient := AmbientOptions{TokenBudget: opts.AmbientTokenBudget, MaxCharsPerMessage: opts.AmbientMaxChars}
	if ambient.TokenBudget == 0 {
		ambient.TokenBudget = DefaultAmbientTokenBudget
	}
	if ambient.MaxCharsPerMessage == 0 {
		ambient.MaxCharsPerMessage = DefaultAmbientMaxChars
	}
	return &Assembler{prefix: system, hash: HashText(system), max: opts.MaxHistory, ambient: ambient, halfStatic: opts.HalfStatic}
}

// Prefix 返回不可变前缀正文。
func (a *Assembler) Prefix() string { return a.prefix }

// PrefixHash 返回不可变前缀的短摘要，用于在日志里证明前缀跨轮未变。
func (a *Assembler) PrefixHash() string { return a.hash }

// Build 是 BuildFor 的无会话上下文形式（测试与不关心人格的调用方使用）。
func (a *Assembler) Build(hist []history.Item, memoryBlock, user string) []llm.Message {
	return a.BuildFor(context.Background(), session.Key{}, hist, memoryBlock, user)
}

// BuildFor 装配 [静态段+半静态段] + [记忆] + [只追加历史] + [当前输入]。
//
// 顺序即不变量：前缀永远在最先，当前输入永远在最后；记忆紧随前缀，
// 历史只按原顺序追加（见 ADR-0002）。
//
// key 决定半静态段的内容（F-65：当前会话的人格设定）。人格只改变静态段
// **之后**的字节，因此不同人格的会话仍共享同一段静态前缀。
//
// **这是消息序列的唯一装配点。** agent 不再自己拼消息：一旦它自己拼，
// 窗口与环境消息预算就只会在测试里生效，而线上永远不生效——
// 这个模块被绕开过一次，真机上就是这么坏掉的。
func (a *Assembler) BuildFor(ctx context.Context, key session.Key, hist []history.Item, memoryBlock, user string) []llm.Message {
	items := a.compress(hist)
	out := make([]llm.Message, 0, len(items)+3)
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: a.SystemFor(ctx, key)})
	// ADR-0002：记忆是独立消息，放在 system 之后、历史之前。
	// 不进 system 是为了保住 system 段的全局缓存；不放到最后是为了让记忆本身也能被缓存。
	if memoryBlock != "" {
		out = append(out, llm.Message{Role: llm.RoleSystem, Content: memoryBlock})
	}
	out = append(out, ToMessages(items)...)
	out = append(out, llm.Message{Role: llm.RoleUser, Content: user})
	return out
}

// SystemFor 返回某会话本轮 system 消息的正文：静态段 + 半静态段（人格设定）。
//
// 半静态段为空时逐字节等于静态段——未配置人格的部署因此与旧行为完全一致。
func (a *Assembler) SystemFor(ctx context.Context, key session.Key) string {
	if a.halfStatic == nil {
		return a.prefix
	}
	half := strings.TrimSpace(a.halfStatic(ctx, key))
	if half == "" {
		return a.prefix
	}
	return a.prefix + "\n\n" + half
}

// Segment 是一段提示词的名字与哈希（F-65 的 /prompt-hash 用它）。
type Segment struct {
	Name string
	Hash string
}

// Segments 返回三段式提示词的哈希报告（F-65）。
//
// 静态段是 system 消息里的静态正文；工具 schema 不在其中——它随请求的
// Tools 字段发送，顺序由 F-41 的注册顺序固定，因此不在本函数的职责内。
// 半静态段含人格设定与长期记忆（同属小时级变化，合成一段报告）。
// 动态段每轮都变，给它算"当前值"只会让人误以为可以比对，因此哈希固定为 "-"。
func (a *Assembler) Segments(ctx context.Context, key session.Key, memoryBlock string) []Segment {
	half := ""
	if a.halfStatic != nil {
		half = strings.TrimSpace(a.halfStatic(ctx, key))
	}
	halfHash := HashText(half + "\x00" + memoryBlock)
	return []Segment{
		{Name: "static", Hash: a.hash},
		{Name: "half-static", Hash: halfHash},
		{Name: "dynamic", Hash: "-"},
	}
}

// HashText 返回文本的短摘要（供段哈希使用）。
func HashText(s string) string { return shortHash(s) }

// compress 按"对话按轮次、环境按 token 预算"分别裁剪，再按**原序**合并。
//
// 两类分开是刻意的：群里刷屏几分钟就能把真正的对话挤出窗口。
// 环境消息是背景，它该受 token 预算约束；与机器人的对话才是主体，按轮次保留。
//
// 用两遍扫描而不是各裁各的再拼接：拼接会让两类消息的相对顺序错乱，
// 而顺序本身就是模型理解"谁先说的"的依据。
func (a *Assembler) compress(items []history.Item) []history.Item {
	if len(items) == 0 {
		return items
	}
	var convo, ambient []history.Item
	for _, it := range items {
		if !it.Ambient {
			convo = append(convo, it)
			continue
		}
		ambient = append(ambient, it)
	}

	keptConvo := trimHistory(convo, a.max)
	convoDropped := len(convo) - len(keptConvo)

	_, ambientDropped := CompressAmbient(ambient, a.ambient)

	// 第二遍：按原序输出，对话与环境的裁剪线各按各的。
	out := make([]history.Item, 0, len(items))
	convoSeen, ambientSeen := 0, 0
	for _, it := range items {
		if !it.Ambient {
			convoSeen++
			if convoSeen > convoDropped {
				out = append(out, it)
			}
			continue
		}
		ambientSeen++
		if ambientSeen > ambientDropped {
			out = append(out, it)
		}
	}
	// 环境消息还要走一遍截断与刷屏合并（它们只影响渲染，不影响保留决策）。
	return finalizeAmbient(out, a.ambient)
}

// finalizeAmbient 对环境消息做截断与刷屏合并，对话消息原样保留。
func finalizeAmbient(items []history.Item, opts AmbientOptions) []history.Item {
	var convo, ambient []history.Item
	for _, it := range items {
		if !it.Ambient {
			convo = append(convo, it)
			continue
		}
		ambient = append(ambient, it)
	}
	if len(ambient) == 0 {
		return items
	}
	compressed, _ := CompressAmbient(ambient, opts)
	// 按原序重新拼接：用内容+时间做键恢复相对位置过于脆弱，
	// 因此这里直接遍历原序列，遇到环境条目就按消费顺序取压缩结果。
	out := make([]history.Item, 0, len(convo)+len(compressed))
	ai := 0
	for _, it := range items {
		if !it.Ambient {
			out = append(out, it)
			continue
		}
		if ai < len(compressed) {
			out = append(out, compressed[ai])
			ai++
		}
		// 被合并掉的重复条目在此跳过——它们已经计入上一条的计数。
	}
	return out
}

// ToMessages 把历史条目转成消息序列。
//
// 只供本包的装配使用（也包括测试）。映射规则必须只有一份：
// 历史形状一旦漂移，前缀缓存就会静默失效。
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
		// 发言人标签与时间在**这里**渲染，而不是写库时烤进正文：
		// 检索返回的正文因此保持干净，全文索引也不被前缀污染。
		return llm.Message{Role: llm.RoleUser, Content: it.RenderText()}, true
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
