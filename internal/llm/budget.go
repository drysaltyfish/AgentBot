package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"unicode"
)

// Counter 统计一组消息的 token 数（F-32）。
//
// 实现优先级：provider 返回的真实 usage > 本地 tokenizer（若可用）> 启发式估算。
// MeasuredCounter 负责把实测值喂回来，HeuristicCounter 是永远可用的兜底。
type Counter interface {
	Count(ctx context.Context, model string, msgs []Message) (int, error)
}

// CounterFunc 把函数适配成 Counter。
type CounterFunc func(ctx context.Context, model string, msgs []Message) (int, error)

// Count 实现 Counter。
func (f CounterFunc) Count(ctx context.Context, model string, msgs []Message) (int, error) {
	return f(ctx, model, msgs)
}

// HeuristicCounter 是纯计算的兜底估算器：中文约 1.5 字符/token、英文约 4 字符/token。
//
// 不引分词器：只为裁剪预算，纯计算、零依赖、可预测，比字节数估算准得多。
type HeuristicCounter struct{}

// Count 实现 Counter。
func (HeuristicCounter) Count(ctx context.Context, model string, msgs []Message) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return estimateMessages(msgs), nil
}

// EstimateTokens 按启发式估算一段文本的 token 数。
//
// 中文约 1.5 字符/token（ceil(cjk/1.5) = ceil(2*cjk/3)），英文约 4 字符/token；
// 向上取整，宁可高估也不要因低估而超预算。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk, ascii := 0, 0
	for _, r := range text {
		if r < unicode.MaxASCII {
			ascii++
			continue
		}
		cjk++
	}
	return (2*cjk+2)/3 + (ascii+3)/4
}

// EstimateMessageTokens 估算一条消息（含角色、工具调用等所有会被序列化的文本）。
func EstimateMessageTokens(m Message) int {
	// 每条消息的固定结构开销（role 分隔等）。
	n := 4
	n += EstimateTokens(string(m.Role))
	n += EstimateTokens(m.Content)
	n += EstimateTokens(m.ReasoningContent)
	n += EstimateTokens(m.Name)
	n += EstimateTokens(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		n += EstimateTokens(tc.ID) + EstimateTokens(tc.Name) + EstimateTokens(tc.Arguments)
	}
	return n
}

func estimateMessages(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateMessageTokens(m)
	}
	return total
}

func estimateTools(tools []ToolSpec) int {
	total := 0
	for _, t := range tools {
		total += 4 + EstimateTokens(t.Name) + EstimateTokens(t.Description) + EstimateTokens(string(t.Parameters))
	}
	return total
}

// MeasuredCounter 优先返回 provider 实测的 prompt token，其次回退到注入的
// 本地 tokenizer / 启发式估算（F-32 的实现优先级）。
//
// 实测值按消息序列指纹缓存：前缀不变时直接复用上次的 prompt_tokens。缓存只存
// 哈希与数量，不存提示词正文。并发安全。
type MeasuredCounter struct {
	fallback Counter

	mu     sync.Mutex
	actual map[string]int
}

// NewMeasuredCounter 构造；fallback 为 nil 时使用 HeuristicCounter。
func NewMeasuredCounter(fallback Counter) *MeasuredCounter {
	if fallback == nil {
		fallback = HeuristicCounter{}
	}
	return &MeasuredCounter{fallback: fallback, actual: make(map[string]int)}
}

// Count 实现 Counter。
func (c *MeasuredCounter) Count(ctx context.Context, model string, msgs []Message) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	key := messageFingerprint(model, msgs)
	c.mu.Lock()
	n, ok := c.actual[key]
	c.mu.Unlock()
	if ok {
		return n, nil
	}
	return c.fallback.Count(ctx, model, msgs)
}

// Observe 记录一次真实 usage，供后续相同前缀的 Count 直接命中。
func (c *MeasuredCounter) Observe(model string, msgs []Message, u Usage) {
	if u.PromptTokens <= 0 || len(msgs) == 0 {
		return
	}
	key := messageFingerprint(model, msgs)
	c.mu.Lock()
	c.actual[key] = u.PromptTokens
	c.mu.Unlock()
}

func messageFingerprint(model string, msgs []Message) string {
	h := sha256.New()
	writeField := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	writeField(model)
	for _, m := range msgs {
		writeField(string(m.Role))
		writeField(m.Content)
		writeField(m.ReasoningContent)
		writeField(m.Name)
		writeField(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			writeField(tc.ID)
			writeField(tc.Name)
			writeField(tc.Arguments)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Summarizer 把被裁掉的消息压缩成一条摘要（F-32 可选接缝）。
//
// ok 为 false 表示本次不产出摘要，调用方退回纯裁剪；error 视为非致命，
// 由 Budget.OnError 上报后仍按纯裁剪继续。
type Summarizer interface {
	Summarize(ctx context.Context, dropped []Message) (Message, bool, error)
}

// Budget 是上下文预算（F-32）。
//
// 可用输入 = MaxContext - ReserveOutput - ReserveTools。
//
// # G5 决策：谁先裁剪（未裁定分歧的落地选择）
//
// 结论：**pinned 消息永不裁剪；其余消息一律按“最旧优先”整组裁剪（滑动窗口）。**
//
// 理由：
//  1. ADR-0002 的布局把 system 作为全局共享的稳定前缀；裁掉 system 会让所有会话
//     的缓存前缀同时失效，代价远大于丢失最旧的历史。
//  2. system 承载行为契约与工具协议，丢失会改变模型行为；历史是只追加的尾部，
//     丢最旧的只损失相关性最低的上下文。
//  3. 最旧优先的滑动窗口在 prefix.ComparePrefix 中被归类为 RelationSlid（预期变化），
//     而裁 system 会变成 diverged（意料之外的前缀改写），触发告警。
//
// 工具调用与其 tool 结果按组同进同退，绝不拆散（否则服务端会 400）。
//
// 注意：若 pinned 消息自身就超过预算，Fit 无法再裁，此时原样保留 pinned
// 并交由调用方调整 Reserve 配置——pinned 的神圣性优先于预算。
type Budget struct {
	MaxContext    int
	ReserveOutput int
	ReserveTools  int

	// Counter 为空时使用 HeuristicCounter。可通过 MeasuredCounter 接入实测值。
	Counter Counter
	// Model 透传给 Counter。
	Model string
	// Summarizer 可选：被裁掉的消息压缩成一条摘要，插在 pinned 前缀之后、历史之前
	// （与 ADR-0002 的注入位置一致）。默认关闭；开启后摘要必须确定性，否则会引入
	// 意料之外的前缀变化。
	Summarizer Summarizer
	// OnError 可选：摘要失败等非致命错误在此上报。
	OnError func(error)
}

// InputLimit 返回可用输入 token 数。
func (b Budget) InputLimit() int {
	limit := b.MaxContext - b.ReserveOutput - b.ReserveTools
	if limit < 0 {
		return 0
	}
	return limit
}

func (b Budget) counter() Counter {
	if b.Counter == nil {
		return HeuristicCounter{}
	}
	return b.Counter
}

func (b Budget) count(ctx context.Context, msgs []Message) (int, error) {
	return b.counter().Count(ctx, b.Model, msgs)
}

// ToolTokens 统计工具 schema 占用的 token（F-32 边界：工具 schema 必须计入预算）。
func (b Budget) ToolTokens(ctx context.Context, tools []ToolSpec) (int, error) {
	if len(tools) == 0 {
		return 0, nil
	}
	msgs := make([]Message, 0, len(tools))
	for _, t := range tools {
		msgs = append(msgs, Message{Role: RoleSystem, Content: t.Name + " " + t.Description + " " + string(t.Parameters)})
	}
	return b.count(ctx, msgs)
}

// Fit 在超预算时裁剪消息（F-32）。返回新切片，不修改入参。
func (b Budget) Fit(ctx context.Context, msgs []Message) []Message {
	if b.MaxContext <= 0 {
		return cloneMessages(msgs)
	}
	return b.fit(ctx, msgs, 0)
}

// FitRequest 在组装请求前裁剪：同时把工具 schema 计入预算。
//
// 实际工具占用超过 ReserveTools 时按实际值预留，避免“预留不足→超限被拒”。
func (b Budget) FitRequest(ctx context.Context, req *ChatRequest) *ChatRequest {
	if req == nil {
		return nil
	}
	toolTokens, err := b.ToolTokens(ctx, req.Tools)
	if err != nil {
		// 计量失败不能阻断主流程：退回启发式。
		toolTokens = estimateTools(req.Tools)
	}
	reserve := b.ReserveTools
	if toolTokens > reserve {
		reserve = toolTokens
	}
	extra := reserve - b.ReserveTools
	if extra < 0 {
		extra = 0
	}
	out := *req
	out.Messages = b.fit(ctx, req.Messages, extra)
	return &out
}

func (b Budget) fit(ctx context.Context, msgs []Message, extraReserve int) []Message {
	if b.MaxContext <= 0 || len(msgs) == 0 {
		return cloneMessages(msgs)
	}
	limit := b.InputLimit() - extraReserve
	if limit < 0 {
		limit = 0
	}
	groups := buildGroups(msgs)
	total := 0
	for i := range groups {
		cost, err := b.count(ctx, msgs[groups[i].start:groups[i].end])
		if err != nil {
			cost = estimateMessages(msgs[groups[i].start:groups[i].end])
		}
		groups[i].cost = cost
		total += cost
	}
	if total <= limit {
		return cloneMessages(msgs)
	}
	dropped := make([]bool, len(groups))
	var droppedMsgs []Message
	for i := range groups {
		if total <= limit {
			break
		}
		if groups[i].pinned {
			continue
		}
		dropped[i] = true
		total -= groups[i].cost
		droppedMsgs = append(droppedMsgs, msgs[groups[i].start:groups[i].end]...)
	}
	var summary *Message
	if b.Summarizer != nil && len(droppedMsgs) > 0 {
		if m, ok, err := b.Summarizer.Summarize(ctx, droppedMsgs); err != nil {
			if b.OnError != nil {
				b.OnError(err)
			}
		} else if ok {
			summary = &m
		}
	}
	out := make([]Message, 0, len(msgs))
	inserted := false
	for i := range groups {
		if dropped[i] {
			if summary != nil && !inserted {
				out = append(out, *summary)
				inserted = true
			}
			continue
		}
		out = append(out, msgs[groups[i].start:groups[i].end]...)
	}
	if summary != nil && !inserted {
		out = append(out, *summary)
	}
	return out
}

type msgGroup struct {
	start, end int
	pinned     bool
	cost       int
}

// buildGroups 把消息切成不可拆分的组：带 tool_calls 的 assistant 与其后的
// tool 结果同属一组，其余消息各自成组。组内任一消息 pinned，整组 pinned。
func buildGroups(msgs []Message) []msgGroup {
	groups := make([]msgGroup, 0, len(msgs))
	for i := 0; i < len(msgs); {
		end := i + 1
		pinned := msgs[i].Pinned
		if msgs[i].Role == RoleAssistant && len(msgs[i].ToolCalls) > 0 {
			ids := make(map[string]struct{}, len(msgs[i].ToolCalls))
			for _, tc := range msgs[i].ToolCalls {
				ids[tc.ID] = struct{}{}
			}
			for end < len(msgs) && msgs[end].Role == RoleTool {
				if _, ok := ids[msgs[end].ToolCallID]; !ok {
					break
				}
				if msgs[end].Pinned {
					pinned = true
				}
				end++
			}
		}
		groups = append(groups, msgGroup{start: i, end: end, pinned: pinned})
		i = end
	}
	return groups
}

func cloneMessages(msgs []Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out
}

// UsageStats 是累计用量（F-32，供 F-66 使用）。
type UsageStats struct {
	Calls                 int
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
	ReasoningTokens       int
}

// Add 累加一次用量。
func (s *UsageStats) Add(u Usage) {
	s.Calls++
	s.PromptTokens += u.PromptTokens
	s.CompletionTokens += u.CompletionTokens
	s.TotalTokens += u.TotalTokens
	s.PromptCacheHitTokens += u.PromptCacheHitTokens
	s.PromptCacheMissTokens += u.PromptCacheMissTokens
	s.ReasoningTokens += u.ReasoningTokens
}

// UsageTracker 按会话与全局聚合用量，并记录估算与实测的偏差用于校准系数。
//
// 并发安全；计数单调不减。不存提示词全文，避免表/内存膨胀。
type UsageTracker struct {
	mu        sync.Mutex
	global    UsageStats
	sessions  map[string]UsageStats
	estimated int
	actual    int
	samples   int
}

// NewUsageTracker 构造。
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{sessions: make(map[string]UsageStats)}
}

// Record 记录一次调用。estimated 为发送前的估算输入 token（<=0 表示未估算），
// u 为 provider 返回的真实 usage。
func (t *UsageTracker) Record(session string, estimated int, u Usage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.global.Add(u)
	if session != "" {
		s := t.sessions[session]
		s.Add(u)
		t.sessions[session] = s
	}
	if estimated > 0 {
		t.estimated += estimated
		t.actual += u.PromptTokens
		t.samples++
	}
}

// Global 返回全局累计用量。
func (t *UsageTracker) Global() UsageStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.global
}

// Session 返回某个会话的累计用量。
func (t *UsageTracker) Session(key string) UsageStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[key]
}

// Sessions 返回全部会话用量的副本。
func (t *UsageTracker) Sessions() map[string]UsageStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]UsageStats, len(t.sessions))
	for k, v := range t.sessions {
		out[k] = v
	}
	return out
}

// Calibration 返回“实测 prompt token / 估算值”的累计比值与样本数。
//
// 比值 >1 表示启发式低估、<1 表示高估，用于校准 F-32 的启发式系数。
// 无样本时返回 (0, 0)。
func (t *UsageTracker) Calibration() (float64, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.estimated <= 0 {
		return 0, t.samples
	}
	return float64(t.actual) / float64(t.estimated), t.samples
}
