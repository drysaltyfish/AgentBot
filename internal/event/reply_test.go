package event

import (
	"encoding/json"
	"strings"
	"testing"
)

// Test_QuotedReplyIsRenderedAndResolvable 覆盖 QQ 引用功能的解析与渲染。
func Test_QuotedReplyIsRenderedAndResolvable(t *testing.T) {
	t.Parallel()
	raw := `[{"type":"reply","data":{"id":"12345"}},{"type":"text","data":{"text":"这句什么意思"}}]`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// 未解析前：知道有引用，但不知道内容。
	if got := m.Summary(); !strings.Contains(got, "[回复]") {
		t.Fatalf("未解析时应保留占位符: %q", got)
	}
	if !strings.Contains(m.Summary(), "这句什么意思") {
		t.Fatalf("正文必须保留: %q", m.Summary())
	}

	ids := m.ReplyIDs()
	if len(ids) != 1 || ids[0] != "12345" {
		t.Fatalf("应取出被引用消息 id: %v", ids)
	}

	// 解析后：把被引用的内容填进去。
	if n := m.SetReplyText("12345", "你刚才说记住了"); n != 1 {
		t.Fatalf("应填充 1 段: %d", n)
	}
	got := m.Summary()
	if !strings.Contains(got, "[回复 你刚才说记住了]") {
		t.Fatalf("应显示被引用内容: %q", got)
	}
	if !strings.Contains(got, "这句什么意思") {
		t.Fatalf("正文必须保留: %q", got)
	}
}

func Test_SetReplyTextIgnoresUnrelatedAndEmpty(t *testing.T) {
	t.Parallel()
	m := Message{Reply("999"), Text("正文")}
	if n := m.SetReplyText("other", "x"); n != 0 {
		t.Fatalf("id 不匹配不该改动: %d", n)
	}
	if n := m.SetReplyText("999", "   "); n != 0 {
		t.Fatalf("空内容不该填充: %d", n)
	}
	if n := m.SetReplyText("", "x"); n != 0 {
		t.Fatalf("空 id 不该填充: %d", n)
	}
	if got := m.Summary(); !strings.Contains(got, "[回复]") {
		t.Fatalf("未填充时应是占位符: %q", got)
	}
}

func Test_ReplyIDsSkipsEmptyIDs(t *testing.T) {
	t.Parallel()
	m := Message{Reply("a"), Reply(""), Reply("b")}
	ids := m.ReplyIDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("应跳过空 id: %v", ids)
	}
}
