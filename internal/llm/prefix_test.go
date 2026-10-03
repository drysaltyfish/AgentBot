package llm

import (
	"strings"
	"testing"
)

func msg(role Role, content string) Message { return Message{Role: role, Content: content} }

// Test_F89_ComparePrefixClassifies 覆盖四种关系。
func Test_F89_ComparePrefixClassifies(t *testing.T) {
	t.Parallel()
	base := []string{"a", "b", "c"}

	if got := ComparePrefix(base, []string{"a", "b", "c"}); got.Relation != RelationIdentical {
		t.Fatalf("相同应判为 identical: %+v", got)
	}
	ext := ComparePrefix(base, []string{"a", "b", "c", "d"})
	if ext.Relation != RelationExtended || ext.CommonPrefix != 3 {
		t.Fatalf("追加应判为 extended: %+v", ext)
	}
	slid := ComparePrefix(base, []string{"b", "c"})
	if slid.Relation != RelationSlid || slid.SlidBy != 1 {
		t.Fatalf("窗口滑动应判为 slid: %+v", slid)
	}
	slid2 := ComparePrefix([]string{"a", "b", "c", "d"}, []string{"c", "d", "e"})
	if slid2.Relation != RelationSlid || slid2.SlidBy != 2 {
		t.Fatalf("滑动 2 条: %+v", slid2)
	}
	// 中间被改写：既不是追加也不是滑动。
	div := ComparePrefix(base, []string{"a", "X", "c"})
	if !div.Unexpected() || div.CommonPrefix != 1 {
		t.Fatalf("改写应判为 diverged 且指出分歧点: %+v", div)
	}
	// 直接截断但不滑动（前缀变短且不为空）——当前判为 diverged：
	// 它是意料之外的变化，值得报出来。
	shorter := ComparePrefix(base, []string{"a", "b"})
	if shorter.Relation != RelationDiverged {
		t.Fatalf("前缀变短应当作异常变化报出: %+v", shorter)
	}
}

func Test_F89_DigestIsPerMessageAndStable(t *testing.T) {
	t.Parallel()
	msgs := []Message{msg(RoleSystem, "S"), msg(RoleUser, "你好")}
	d1 := Digest(msgs)
	d2 := Digest(msgs)
	if strings.Join(d1, ",") != strings.Join(d2, ",") {
		t.Fatalf("同样输入必须同指纹")
	}
	if len(d1) != 2 {
		t.Fatalf("应逐条一个指纹: %d", len(d1))
	}
	// 改一条内容只应影响那一条的指纹。
	changed := Digest([]Message{msg(RoleSystem, "S"), msg(RoleUser, "你好呀")})
	if changed[0] != d1[0] {
		t.Fatalf("未改动的消息指纹不应变化")
	}
	if changed[1] == d1[1] {
		t.Fatalf("改动过的消息指纹必须变化")
	}
}

// Test_F89_DigestDistinguishesToolCalls 守住"工具调用也参与指纹"。
func Test_F89_DigestDistinguishesToolCalls(t *testing.T) {
	t.Parallel()
	a := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "calculator", Arguments: `{"expr":"1+1"}`}}}}
	b := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "calculator", Arguments: `{"expr":"2+2"}`}}}}
	if Digest(a)[0] == Digest(b)[0] {
		t.Fatalf("参数不同的工具调用不得同指纹")
	}
	// 工具结果 id 也参与。
	c := []Message{{Role: RoleTool, ToolCallID: "c1", Content: "ok"}}
	d := []Message{{Role: RoleTool, ToolCallID: "c2", Content: "ok"}}
	if Digest(c)[0] == Digest(d)[0] {
		t.Fatalf("不同 tool_call_id 不得同指纹")
	}
}

func Test_F89_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	d := []string{"a", "b", "c"}
	if got := DecodeDigest(EncodeDigest(d)); strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("编解码往返失败: %v", got)
	}
	if DecodeDigest("") != nil {
		t.Fatalf("空串应解出 nil")
	}
}
