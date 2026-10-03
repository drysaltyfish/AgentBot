package conversation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

func user(s string) history.Item   { return history.Item{Kind: history.KindUser, Content: s} }
func asst(s string) history.Item   { return history.Item{Kind: history.KindAssistant, Content: s} }
func marker(s string) history.Item { return history.Item{Kind: history.KindMarker, Content: s} }

// assertPrefix 断言 prev 是 next 的前缀（逐字段比较）。
//
// 这正是 DeepSeek 前缀缓存的命中条件：下一轮请求必须完整匹配上一轮的缓存前缀单元。
func assertPrefix(t *testing.T, prev, next []llm.Message) {
	t.Helper()
	if len(prev) > len(next) {
		t.Fatalf("previous turn has more messages (%d) than next (%d)", len(prev), len(next))
	}
	for i, m := range prev {
		if next[i].Role != m.Role || next[i].Content != m.Content {
			t.Fatalf("prefix broken at index %d: previous=(%q,%q) next=(%q,%q)",
				i, m.Role, m.Content, next[i].Role, next[i].Content)
		}
	}
}

// Test_CacheFirst_PreviousTurnIsPrefixOfNext 是这套设计最重要的不变量。
func Test_CacheFirst_PreviousTurnIsPrefixOfNext(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "你是助手"})

	hist := []history.Item{}
	m1 := a.Build(hist, "你好")

	hist = append(hist, user("你好"), asst("你也好"))
	m2 := a.Build(hist, "今天天气怎么样")

	hist = append(hist, user("今天天气怎么样"), asst("挺好的"))
	m3 := a.Build(hist, "再见")

	assertPrefix(t, m1, m2)
	assertPrefix(t, m2, m3)

	// 当前输入永远在最后，前缀永远在最先。
	if m3[0].Role != llm.RoleSystem || m3[0].Content != "你是助手" {
		t.Fatalf("system prefix must stay first: %+v", m3[0])
	}
	if last := m3[len(m3)-1]; last.Role != llm.RoleUser || last.Content != "再见" {
		t.Fatalf("current input must stay last: %+v", last)
	}
}

// Test_CacheFirst_MarkersNeverGoUpstream 保证内部草稿不会污染上行前缀。
func Test_CacheFirst_MarkersNeverGoUpstream(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "S"})
	hist := []history.Item{
		user("问题"),
		marker("内部草稿：先想一下"),
		asst("回答"),
		marker("内部草稿：再想一下"),
	}
	msgs := a.Build(hist, "下一个问题")

	for _, m := range msgs {
		if strings.Contains(m.Content, "内部草稿") {
			t.Fatalf("marker leaked upstream: %+v", m)
		}
	}
	// 且 marker 的存在不影响前缀性质。
	withoutMarkers := a.Build([]history.Item{user("问题"), asst("回答")}, "下一个问题")
	if Fingerprint(msgs) != Fingerprint(withoutMarkers) {
		t.Fatalf("markers changed the assembled messages: with=%s without=%s",
			Fingerprint(msgs), Fingerprint(withoutMarkers))
	}
}

// Test_CacheFirst_PrefixHashIsStable 证明前缀在 Assembler 生命周期内不变。
func Test_CacheFirst_PrefixHashIsStable(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "固定的前缀"})
	h := a.PrefixHash()
	for i := 0; i < 3; i++ {
		_ = a.Build([]history.Item{user(fmt.Sprintf("第 %d 轮", i))}, "现在")
		if a.PrefixHash() != h {
			t.Fatalf("prefix hash changed across calls: %s -> %s", h, a.PrefixHash())
		}
	}
	if New(Options{System: "固定的前缀"}).PrefixHash() != h {
		t.Fatalf("same system prompt must hash the same")
	}
	if New(Options{System: "另一个前缀"}).PrefixHash() == h {
		t.Fatalf("different system prompts must not collide")
	}
}

// Test_CacheFirst_DefaultPrefixHasNoVolatileContent 守住"前缀里不许有易变内容"。
func Test_CacheFirst_DefaultPrefixHasNoVolatileContent(t *testing.T) {
	t.Parallel()
	a := New(Options{})
	if a.Prefix() != DefaultSystemPrompt {
		t.Fatalf("empty system prompt must fall back to the default")
	}
	// 两次构造必须逐字节相同：默认前缀里不允许出现时间戳/随机数/计数器。
	if New(Options{}).PrefixHash() != a.PrefixHash() {
		t.Fatalf("default prefix is not deterministic")
	}
	for _, bad := range []string{"2026", "{{", "%s", "time.Now"} {
		if strings.Contains(a.Prefix(), bad) {
			t.Fatalf("default prefix looks volatile (contains %q)", bad)
		}
	}
}

// Test_CacheFirst_TrimAlignsToTurnBoundary 覆盖超限裁剪的对齐行为。
func Test_CacheFirst_TrimAlignsToTurnBoundary(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "S", MaxHistory: 3})
	hist := []history.Item{
		user("u1"), asst("a1"),
		user("u2"), asst("a2"),
		user("u3"), asst("a3"),
	}
	msgs := a.Build(hist, "u4")
	// 裁剪后必须从 user 开始，绝不能从 assistant 开始。
	if msgs[1].Role != llm.RoleUser {
		t.Fatalf("trimmed window must start at a user turn: %+v", msgs[1])
	}
	if len(msgs) > 3+2 {
		t.Fatalf("window not applied: %d messages", len(msgs))
	}
	// 未超限时不得裁剪（窗口内前缀稳定）。
	small := a.Build([]history.Item{user("u1"), asst("a1")}, "u2")
	if len(small) != 4 {
		t.Fatalf("under-limit history must not be trimmed: %d messages", len(small))
	}
}

// Test_CacheFirst_ToolItemsRoundTrip 覆盖工具相关条目的映射。
func Test_CacheFirst_ToolItemsRoundTrip(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "S"})
	hist := []history.Item{
		user("查一下"),
		{Kind: history.KindToolCall, ToolCalls: []history.ToolCall{{ID: "c1", Name: "search", Arguments: "{}"}}},
		{Kind: history.KindToolResult, ToolCallID: "c1", Content: "结果"},
	}
	msgs := a.Build(hist, "继续")
	if msgs[2].Role != llm.RoleAssistant || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].ID != "c1" {
		t.Fatalf("tool_call mapping wrong: %+v", msgs[2])
	}
	if msgs[3].Role != llm.RoleTool || msgs[3].ToolCallID != "c1" {
		t.Fatalf("tool_result mapping wrong: %+v", msgs[3])
	}
}
