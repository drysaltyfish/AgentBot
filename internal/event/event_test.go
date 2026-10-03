package event

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	groupMsgJSON    = `{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":1234567890123456789,"time":1750000000,"sender":{"user_id":20002,"nickname":"nick","card":"card","role":"admin"},"message":[{"type":"text","data":{"text":"hello"}},{"type":"at","data":{"qq":"10001"}}]}`
	privateMsgJSON  = `{"post_type":"message","message_type":"private","sub_type":"friend","self_id":10001,"user_id":20002,"message_id":"abcd-efgh","time":1750000001,"sender":{"user_id":20002,"nickname":"nick","role":"member"},"message":[{"type":"text","data":{"text":"hi"}}]}`
	messageSentJSON = `{"post_type":"message_sent","message_type":"group","sub_type":"normal","self_id":10001,"user_id":10001,"group_id":30003,"message_id":42,"time":1750000002,"sender":{"user_id":10001},"message":[{"type":"text","data":{"text":"out"}}]}`
	pokeJSON        = `{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":10001,"user_id":20002,"group_id":30003,"time":1750000003}`
	requestJSON     = `{"post_type":"request","request_type":"friend","user_id":20002,"comment":"hi","flag":"f1","time":1750000004}`
	metaJSON        = `{"post_type":"meta_event","meta_event_type":"lifecycle","sub_type":"connect","self_id":10001,"time":1750000005}`
)

func Test_F01_KindSubMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		raw        string
		wantKind   Kind
		wantSub    string
		wantSubSub string
	}{
		{"group message", groupMsgJSON, KindMessage, "group", "normal"},
		{"private message", privateMsgJSON, KindMessage, "private", "friend"},
		{"message_sent normalizes to message", messageSentJSON, KindMessage, "group", "normal"},
		{"notice poke", pokeJSON, KindNotice, "notify", "poke"},
		{"request friend", requestJSON, KindRequest, "friend", ""},
		{"meta lifecycle", metaJSON, KindMeta, "lifecycle", "connect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvent([]byte(tc.raw))
			if e.Kind != tc.wantKind {
				t.Fatalf("Kind: actual=%q expected=%q", e.Kind, tc.wantKind)
			}
			if e.Sub != tc.wantSub {
				t.Fatalf("Sub: actual=%q expected=%q", e.Sub, tc.wantSub)
			}
			if e.SubSub != tc.wantSubSub {
				t.Fatalf("SubSub: actual=%q expected=%q", e.SubSub, tc.wantSubSub)
			}
			if e.Warning() != "" {
				t.Fatalf("unexpected decode warning: %q", e.Warning())
			}
		})
	}
}

func Test_F01_CommonFieldsAndMessage(t *testing.T) {
	t.Parallel()
	e := NewEvent([]byte(groupMsgJSON))
	if e.SelfID != 10001 || e.UserID != 20002 || e.GroupID != 30003 {
		t.Fatalf("ids: actual=(%d,%d,%d) expected=(10001,20002,30003)", e.SelfID, e.UserID, e.GroupID)
	}
	if e.MessageID.Int64() != 1234567890123456789 {
		t.Fatalf("MessageID lost int64 precision: actual=%d expected=1234567890123456789", e.MessageID.Int64())
	}
	if e.Sender.Role != "admin" || e.Sender.Card != "card" {
		t.Fatalf("sender: actual=%+v expected role=admin card=card", e.Sender)
	}
	if e.Time.IsZero() || e.Time.UTC().Unix() != 1750000000 {
		t.Fatalf("time: actual=%v expected=unix 1750000000", e.Time)
	}
	if got := e.Message.PlainText(); got != "hello" {
		t.Fatalf("PlainText: actual=%q expected=%q", got, "hello")
	}
	if len(e.Message) != 2 {
		t.Fatalf("message segments: actual=%d expected=2", len(e.Message))
	}
}

func Test_F01_GetPathAndMissingPath(t *testing.T) {
	t.Parallel()
	e := NewEvent([]byte(groupMsgJSON))

	v, ok := e.Get("sender.card")
	if !ok || v != "card" {
		t.Fatalf("Get(sender.card): actual=(%v,%v) expected=(card,true)", v, ok)
	}
	if _, ok := e.Get("sender.nope"); ok {
		t.Fatalf("Get(sender.nope): actual=found expected=missing")
	}
	if _, ok := e.Get("a.b.c.d"); ok {
		t.Fatalf("Get(deep missing): actual=found expected=missing")
	}
	if got, ok := e.Get("sender.user_id"); !ok || toInt64(got) != 20002 {
		t.Fatalf("Get(sender.user_id): actual=(%v,%v) expected=(20002,true)", got, ok)
	}
	if got, ok := e.Get("message.1.type"); !ok || got != "at" {
		t.Fatalf("Get(message.1.type): actual=(%v,%v) expected=(at,true)", got, ok)
	}
}

func Test_F01_EmptyRawIsSafe(t *testing.T) {
	t.Parallel()
	e := NewEvent(nil)
	if e.Kind != "" {
		t.Fatalf("Kind: actual=%q expected=empty", e.Kind)
	}
	if _, ok := e.Get("anything"); ok {
		t.Fatalf("Get on empty raw: actual=found expected=missing")
	}
	if !e.MessageID.IsZero() {
		t.Fatalf("MessageID: actual=%v expected=zero", e.MessageID)
	}
}

func Test_F01_InvalidJSONRecordsWarningAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	e := NewEvent([]byte("{ not valid json"))
	if e.Warning() == "" {
		t.Fatalf("Warning: actual=empty expected=decode warning")
	}
	if _, ok := e.Get("post_type"); ok {
		t.Fatalf("Get on invalid json: actual=found expected=missing")
	}
}

func Test_F01_OversizedRawIsTruncated(t *testing.T) {
	t.Parallel()
	big := make([]byte, MaxRawSize+1024)
	big[0] = '{'
	for i := 1; i < len(big); i++ {
		big[i] = 'a'
	}
	e := NewEvent(big)
	if !e.RawTruncated {
		t.Fatalf("RawTruncated: actual=false expected=true")
	}
	if len(e.Raw) != MaxRawSize {
		t.Fatalf("len(Raw): actual=%d expected=%d", len(e.Raw), MaxRawSize)
	}
}

func Test_F01_RawIsCopiedNotAliased(t *testing.T) {
	t.Parallel()
	src := []byte(groupMsgJSON)
	e := NewEvent(src)
	src[2] = 'X'
	if e.Kind != KindMessage {
		t.Fatalf("Kind changed after mutating source: actual=%q expected=%q", e.Kind, KindMessage)
	}
	if got, ok := e.Get("message_type"); !ok || got != "group" {
		t.Fatalf("Get after mutating source: actual=(%v,%v) expected=(group,true)", got, ok)
	}
}

func Test_F02_IDConstructionAndEquality(t *testing.T) {
	t.Parallel()
	if !IDFromString("").IsZero() {
		t.Fatalf("empty string should be zero ID")
	}
	if a, b := IDFromString("abc"), IDFromString("abc"); !a.Equal(b) {
		t.Fatalf("same string produced unequal IDs")
	}
	if a, b := IDFromInt64(7), IDFromInt64(7); !a.Equal(b) {
		t.Fatalf("same int produced unequal IDs")
	}
	if IDFromInt64(1).Equal(IDFromInt64(2)) {
		t.Fatalf("different ints compared equal")
	}
	if got := IDFromString("abc").String(); got != "abc" {
		t.Fatalf("String round trip: actual=%q expected=%q", got, "abc")
	}
	if got := IDFromInt64(99).String(); got != "99" {
		t.Fatalf("String for numeric: actual=%q expected=%q", got, "99")
	}
}

func Test_F02_StringIDsNeverCollideWithRealNumericIDs(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"abc", "guild-channel-1", "x", "某频道消息", "0xdeadbeef"} {
		id := IDFromString(s)
		u := uint64(id.Int64())
		if u <= 0xffff_ffff {
			t.Fatalf("IDFromString(%q) landed in the real-numeric-ID range: actual=%d expected>0xffffffff", s, u)
		}
		if id.Int64() <= 0 {
			t.Fatalf("IDFromString(%q) is not positive: actual=%d", s, id.Int64())
		}
	}
}

func Test_F02_MarshalJSONIsSelfAdapting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   ID
		want string
	}{
		{IDFromString("123"), "123"},
		{IDFromString("abc"), `"abc"`},
		{IDFromInt64(0), "0"},
		{IDFromInt64(42), "42"},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Fatalf("Marshal(%q): actual=%s expected=%s", tc.in.String(), got, tc.want)
		}
	}

	for _, raw := range []string{"123", `"abc"`, "0"} {
		var id ID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		round, err := json.Marshal(id)
		if err != nil {
			t.Fatalf("re-Marshal(%s): %v", raw, err)
		}
		if string(round) != raw {
			t.Fatalf("round trip: actual=%s expected=%s", round, raw)
		}
	}
}

func Test_F03_ArrayAndCQFormsAreEqual(t *testing.T) {
	t.Parallel()
	arrayJSON := `[{"type":"at","data":{"qq":"123"}},{"type":"text","data":{"text":" hello "}},{"type":"image","data":{"file":"x.jpg"}}]`
	cq := `[CQ:at,qq=123] hello [CQ:image,file=x.jpg]`

	fromArray, _, err := ParseMessage([]byte(arrayJSON))
	if err != nil {
		t.Fatalf("parse array: %v", err)
	}
	fromCQ, warnings, err := ParseCQString(cq)
	if err != nil {
		t.Fatalf("parse cq: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected CQ warnings: %v", warnings)
	}
	if !reflect.DeepEqual(fromArray, fromCQ) {
		t.Fatalf("array vs CQ mismatch: array=%v cq=%v", fromArray, fromCQ)
	}
}

func Test_F03_RoundTripIsIdempotent(t *testing.T) {
	t.Parallel()
	original := Message{Text("a"), At("1"), Image("f.png"), {Type: "unknown-thing", Data: map[string]string{"k": "v"}}}
	raw := original.Marshal()
	parsed, _, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse marshal output: %v", err)
	}
	if !reflect.DeepEqual(parsed, original) {
		t.Fatalf("round trip mismatch: actual=%v expected=%v", parsed, original)
	}
	first := string(raw)
	second := string(parsed.Marshal())
	if first != second {
		t.Fatalf("second round trip differs: actual=%s expected=%s", second, first)
	}
}

func Test_F03_TableCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		wantText string
		wantSegs int
	}{
		{"empty array", "[]", "", 0},
		{"empty string", `""`, "", 0},
		{"plain text array", `[{"type":"text","data":{"text":"plain"}}]`, "plain", 1},
		{"plain text cq", `"plain"`, "plain", 1},
		{"multi segment", `[{"type":"text","data":{"text":"a"}},{"type":"face","data":{"id":"1"}},{"type":"text","data":{"text":"b"}}]`, "ab", 3},
		{"unknown type kept", `[{"type":"weird","data":{"a":"b"}}]`, "", 1},
		{"escaped text", `[{"type":"text","data":{"text":"a&b[c]d"}}]`, "a&b[c]d", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, err := ParseMessage([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseMessage(%s): %v", tc.in, err)
			}
			if got := m.PlainText(); got != tc.wantText {
				t.Fatalf("PlainText: actual=%q expected=%q", got, tc.wantText)
			}
			if len(m) != tc.wantSegs {
				t.Fatalf("segment count: actual=%d expected=%d (%v)", len(m), tc.wantSegs, m)
			}
		})
	}
}

func Test_F03_CQEscapingRoundTrip(t *testing.T) {
	t.Parallel()
	original := Message{Text("a&b[c]d,e")}
	cq := original.String()
	parsed, _, err := ParseCQString(cq)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(parsed, original) {
		t.Fatalf("escaping round trip: actual=%v expected=%v (cq=%q)", parsed, original, cq)
	}
}

func Test_F03_MalformedCQDoesNotFailWholeMessage(t *testing.T) {
	t.Parallel()
	m, warnings, err := ParseCQString("[CQ:at,qq=1][CQ:,qq=2][CQ:at,broken]after")
	if err != nil {
		t.Fatalf("ParseCQString should not fail hard: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("warnings: actual=0 expected>0")
	}
	if !strings.Contains(m.PlainText(), "after") {
		t.Fatalf("text after a bad segment was lost: actual=%q", m.PlainText())
	}
	if _, _, err := ParseCQString("[CQ:at,qq=1"); err != nil {
		t.Fatalf("unterminated CQ should be tolerated: %v", err)
	}
}

func Test_F03_SegmentStringHidesBase64Payload(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("QUJDREVGR0g=", 20)
	seg := Image("base64://" + payload)
	got := seg.String()
	if strings.Contains(got, payload) {
		t.Fatalf("Segment.String leaked the full base64 payload")
	}
	if !strings.Contains(got, "len=") || !strings.Contains(got, "sha256=") {
		t.Fatalf("Segment.String should show length and hash prefix: actual=%q", got)
	}
	if got := At("123").String(); got != "at{qq=123}" {
		t.Fatalf("At String: actual=%q expected=%q", got, "at{qq=123}")
	}
}

func Test_F03_EmptyMessageIsSafe(t *testing.T) {
	t.Parallel()
	var m Message
	if got := m.PlainText(); got != "" {
		t.Fatalf("PlainText: actual=%q expected=empty", got)
	}
	if got := string(m.Marshal()); got != "[]" {
		t.Fatalf("Marshal: actual=%s expected=[]", got)
	}
	if m2, _, err := ParseMessage(nil); err != nil || len(m2) != 0 {
		t.Fatalf("ParseMessage(nil): actual=(%v,%v) expected=(empty,nil)", m2, err)
	}
}

func Test_F03_SummaryRendersNonTextSegments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"text only", Message{Text("喂")}, "喂"},
		{"face with id", Message{{Type: TypeFace, Data: map[string]string{"id": "4"}}}, "[表情:得意]"},
		{"face without id", Message{{Type: TypeFace, Data: map[string]string{}}}, "[表情]"},
		{"image", Message{Image("x.jpg")}, "[图片]"},
		{"at plus text", Message{At("10001"), Text(" 你好")}, "@10001 你好"},
		{"reply plus text", Message{Reply("42"), Text("ok")}, "[回复]ok"},
		{"unknown type with summary", Message{{Type: "mface", Data: map[string]string{"summary": "动画表情"}}}, "[mface:动画表情]"},
		{"mixed", Message{Text("看"), Image("a.png")}, "看[图片]"},
	}
	for _, tc := range cases {
		if got := tc.msg.Summary(); got != tc.want {
			t.Fatalf("%s: actual=%q expected=%q", tc.name, got, tc.want)
		}
	}

	// 修复要点：纯非文本消息的 PlainText 仍为空（匹配语义不变），
	// 但 Summary 不为空，因此不会再被回复链路静默跳过。
	face := Message{{Type: TypeFace, Data: map[string]string{"id": "4"}}}
	if face.PlainText() != "" {
		t.Fatalf("PlainText must stay empty for a face-only message: actual=%q", face.PlainText())
	}
	if face.Summary() == "" {
		t.Fatalf("Summary must not be empty for a face-only message")
	}
}

// Test_F03_MixedSegmentsWithNonStringDataParse 是回归测试。
//
// 平台对同一字段不保证类型：face.id 是数字、image.file_size 是数字。早期 Segment.Data 是
// map[string]string，任何一个非字符串值都会让整条消息（含文本段）解析失败并变空。
func Test_F03_MixedSegmentsWithNonStringDataParse(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"time":1,"self_id":1,"post_type":"message","message_type":"private","sub_type":"friend","user_id":2,"message_id":3,"message":[{"type":"text","data":{"text":"那我发个"}},{"type":"face","data":{"id":4}},{"type":"text","data":{"text":"这个你不傻了"}},{"type":"image","data":{"file":"a.jpg","file_size":12345,"url":"http://x/y"}},{"type":"mface","data":{"summary":"动画表情","emoji_id":5}}]}`)
	ev := NewEvent(raw)
	if ev.Kind != KindMessage {
		t.Fatalf("kind: actual=%q", ev.Kind)
	}
	if got := ev.Message.PlainText(); got != "那我发个这个你不傻了" {
		t.Fatalf("plain text: actual=%q", got)
	}
	want := "那我发个[表情:得意]这个你不傻了[图片][mface:动画表情]"
	if got := ev.Message.Summary(); got != want {
		t.Fatalf("summary: actual=%q expected=%q", got, want)
	}
	if ev.DecodeWarning != "" {
		t.Fatalf("unexpected decode warning: %s", ev.DecodeWarning)
	}
}

// Test_F03_BrokenSegmentDoesNotDestroyMessage 保证坏段只降级为警告，不毁掉整条消息。
func Test_F03_BrokenSegmentDoesNotDestroyMessage(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"time":1,"self_id":1,"post_type":"message","message_type":"private","user_id":2,"message_id":3,"message":[{"type":"text","data":{"text":"前半"}},"这不是一个对象",{"type":"text","data":{"text":"后半"}}]}`)
	ev := NewEvent(raw)
	if got := ev.Message.PlainText(); got != "前半后半" {
		t.Fatalf("broken segment destroyed the message: actual=%q", got)
	}
	if ev.DecodeWarning == "" {
		t.Fatalf("expected a decode warning for the broken segment")
	}
}

// Test_F03_ScalarCoercion 覆盖 coerceScalar 的类型收敛。
func Test_F03_ScalarCoercion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{`"abc"`, "abc"},
		{`123`, "123"},
		{`-4.5`, "-4.5"},
		{`true`, "true"},
		{`null`, ""},
		{`{"a":1}`, "{\"a\":1}"},
	}
	for _, tc := range cases {
		if got := coerceScalar(json.RawMessage(tc.in)); got != tc.want {
			t.Fatalf("coerceScalar(%s): actual=%q expected=%q", tc.in, got, tc.want)
		}
	}
}

// Test_F03_FaceLabelPrefersPlatformName 覆盖表情名称的三级优先级。
func Test_F03_FaceLabelPrefersPlatformName(t *testing.T) {
	t.Parallel()
	// 平台在 raw.faceText 里直接给出名称时优先用平台的（id 344 表里是"大怨种"，此处故意
	// 给成另一个名字，用来证明取的是平台值而不是表值）。
	platform := Message{{Type: TypeFace, Data: map[string]string{
		"id":  "344",
		"raw": `{"faceIndex":344,"faceText":"/平台新名字"}`,
	}}}
	if got := platform.Summary(); got != "[表情:平台新名字]" {
		t.Fatalf("platform name should win: actual=%q", got)
	}

	// 没有平台名称时用内置表。
	fromTable := Message{{Type: TypeFace, Data: map[string]string{"id": "344"}}}
	if got := fromTable.Summary(); got != "[表情:大怨种]" {
		t.Fatalf("table lookup: actual=%q", got)
	}

	// 表里没有的编号退回编号本身，绝不编造名称。
	unknown := Message{{Type: TypeFace, Data: map[string]string{"id": "999999"}}}
	if got := unknown.Summary(); got != "[表情:999999]" {
		t.Fatalf("unknown id must fall back to the raw id: actual=%q", got)
	}
}

// Test_F03_ImageLabelDistinguishesSticker 覆盖表情包与普通图片的区分。
//
// 字段取值来自真实平台载荷：表情包 sub_type=1/summary=[动画表情]，普通图片 sub_type=0。
func Test_F03_ImageLabelDistinguishesSticker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data map[string]string
		want string
	}{
		{"sticker from real payload", map[string]string{"file": "a.jpg", "sub_type": "1", "summary": "[动画表情]"}, "[动画表情]"},
		{"sticker without summary", map[string]string{"file": "a.jpg", "sub_type": "1"}, "[动画表情]"},
		{"plain image from real payload", map[string]string{"file": "b.jpg", "sub_type": "0", "summary": ""}, "[图片]"},
		{"image without sub_type", map[string]string{"file": "c.jpg"}, "[图片]"},
	}
	for _, tc := range cases {
		msg := Message{{Type: TypeImage, Data: tc.data}}
		if got := msg.Summary(); got != tc.want {
			t.Fatalf("%s: actual=%q expected=%q", tc.name, got, tc.want)
		}
	}
}

// Test_F03_FaceTableIsEmbeddedAndAuthoritative 断言映射表真的被嵌入且内容正确。
func Test_F03_FaceTableIsEmbeddedAndAuthoritative(t *testing.T) {
	t.Parallel()
	if n := FaceCount(); n < 300 {
		t.Fatalf("face table looks missing or truncated: count=%d", n)
	}
	// 抽查三个编号：其中 344 与真实平台载荷中的 faceText 完全一致。
	for id, want := range map[string]string{"0": "惊讶", "4": "得意", "14": "微笑", "344": "大怨种"} {
		got, ok := FaceName(id)
		if !ok {
			t.Fatalf("face %s missing from table", id)
		}
		if got != want {
			t.Fatalf("face %s: actual=%q expected=%q", id, got, want)
		}
	}
	if _, ok := FaceName("999999"); ok {
		t.Fatalf("unknown id should not be in the table")
	}
}

// Test_F03_SummaryFromRealStickerFrame 端到端回归：真实表情包帧必须渲染成动画表情。
func Test_F03_SummaryFromRealStickerFrame(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"time":1,"self_id":1,"post_type":"message","message_type":"private","user_id":2,"message_id":3,"message":[{"type":"image","data":{"file":"41AE7DB0.jpg","file_size":7932,"sub_type":1,"summary":"[动画表情]","url":"https://example/x"}}]}`)
	ev := NewEvent(raw)
	if got := ev.Message.Summary(); got != "[动画表情]" {
		t.Fatalf("sticker frame: actual=%q", got)
	}
	if ev.DecodeWarning != "" {
		t.Fatalf("unexpected decode warning: %s", ev.DecodeWarning)
	}
}
