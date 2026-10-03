package llm

import (
	"fmt"
	"sort"
)

// ToolCallDelta 是流式 tool_call 的一个分片（F-29）。
//
// OpenAI 兼容端点把一次工具调用拆成多个分片：首个分片带 ID 与 Name，
// 后续分片只带 arguments 的增量。Index 标识它属于第几个工具调用。
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// ToolCallAggregator 按 index 聚合流式 tool_call 分片（F-29）。
//
// 只做纯字符串拼接，绝不逐片 json.Unmarshal：arguments 可能被截断在 JSON 的
// 任意位置（引号内、转义序列中间），逐片解析必然失败。
//
// 非并发安全：一个聚合器只服务于一条流（生产者 goroutine）。
type ToolCallAggregator struct {
	// OnWarn 在收到异常分片（同一 index 的 name 重复出现）时被调用；可为 nil。
	OnWarn func(string)

	calls map[int]*ToolCall
	named map[int]bool
}

// NewToolCallAggregator 构造聚合器。
func NewToolCallAggregator() *ToolCallAggregator {
	return &ToolCallAggregator{
		calls: make(map[int]*ToolCall),
		named: make(map[int]bool),
	}
}

// Add 合并一个分片：ID/Name 只在首次出现时设置，Arguments 字符串追加。
func (a *ToolCallAggregator) Add(d ToolCallDelta) {
	if a.calls == nil {
		a.calls = make(map[int]*ToolCall)
	}
	if a.named == nil {
		a.named = make(map[int]bool)
	}
	c, ok := a.calls[d.Index]
	if !ok {
		c = &ToolCall{}
		a.calls[d.Index] = c
	}
	// ID 只在首次出现时设置，避免后续空分片把已有 ID 覆盖掉。
	if c.ID == "" && d.ID != "" {
		c.ID = d.ID
	}
	if d.Name != "" {
		if !a.named[d.Index] {
			c.Name = d.Name
			a.named[d.Index] = true
		} else if a.OnWarn != nil {
			// 同一 index 再次出现 name 属异常：以首次为准，后续仅告警。
			a.OnWarn(fmt.Sprintf("tool_call index %d: duplicate name %q ignored (first=%q)", d.Index, d.Name, c.Name))
		}
	}
	c.Arguments += d.Arguments
}

// Len 返回已出现的工具调用个数。
func (a *ToolCallAggregator) Len() int { return len(a.calls) }

// Finalize 返回按 index 升序排列的完整工具调用。
//
// 幂等：重复调用返回相同结果。流中途出错时调用方应直接丢弃本聚合器、
// 不调用 Finalize，以免执行半截 arguments（F-29 边界：默认丢弃）。
func (a *ToolCallAggregator) Finalize() []ToolCall {
	if len(a.calls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(a.calls))
	for i := range a.calls {
		indexes = append(indexes, i)
	}
	// map 遍历无序，必须显式排序才能保证输出顺序（F-29）。
	sort.Ints(indexes)
	out := make([]ToolCall, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, *a.calls[i])
	}
	return out
}
