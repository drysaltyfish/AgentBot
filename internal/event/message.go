package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 消息段类型常量（FEATURES.md F-03）。
const (
	TypeText     = "text"
	TypeImage    = "image"
	TypeRecord   = "record"
	TypeVideo    = "video"
	TypeAt       = "at"
	TypeFace     = "face"
	TypeReply    = "reply"
	TypeNode     = "node"
	TypeMarkdown = "markdown"
)

// Segment 是消息中的一个段。
type Segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data,omitempty"`
}

// Message 是消息段的有序组合。
type Message []Segment

// Text 构造文本段。
func Text(s string) Segment { return Segment{Type: TypeText, Data: map[string]string{"text": s}} }

// Image 构造图片段。
func Image(file string) Segment {
	return Segment{Type: TypeImage, Data: map[string]string{"file": file}}
}

// At 构造 @ 段。
func At(qq string) Segment { return Segment{Type: TypeAt, Data: map[string]string{"qq": qq}} }

// Reply 构造回复段。
func Reply(id string) Segment { return Segment{Type: TypeReply, Data: map[string]string{"id": id}} }

// Marshal 序列化成 onebot11 的数组形态。
func (m Message) Marshal() json.RawMessage {
	if len(m) == 0 {
		return json.RawMessage("[]")
	}
	out, err := json.Marshal([]Segment(m))
	if err != nil {
		// []Segment 不可能序列化失败；保底返回空数组而不是 panic。
		return json.RawMessage("[]")
	}
	return out
}

// PlainText 抽取纯文本，供关键词/正则匹配使用。
//
// 注意：它只取 text 段——纯表情/纯图片消息会得到空串。需要"给用户看/喂给模型"的
// 文本时请用 Summary。
func (m Message) PlainText() string {
	var b strings.Builder
	for _, seg := range m {
		if seg.Type == TypeText {
			b.WriteString(seg.Data["text"])
		}
	}
	return b.String()
}

// Summary 把整条消息渲染成可读文本：文本段原样保留，非文本段变成占位符。
//
// 与 PlainText 的区别是它不会把非文本消息变成空串——纯表情包/图片也能被回复与记录。
func (m Message) Summary() string {
	var b strings.Builder
	for _, seg := range m {
		switch seg.Type {
		case TypeText:
			b.WriteString(seg.Data["text"])
		case TypeAt:
			b.WriteString("@" + seg.Data["qq"])
		case TypeImage:
			b.WriteString(imageLabel(seg))
		case TypeFace:
			b.WriteString(placeholder("表情", faceLabel(seg)))
		case TypeReply:
			// 被引用的内容由上层解析后写回 data["text"]（见 SetReplyText）。
			// 没有解析到就退回占位符——**宁可显示"有引用但不知道内容"，
			// 也不要假装引用不存在**：那会让模型误以为对方只是随口一说。
			if quoted := strings.TrimSpace(seg.Data["text"]); quoted != "" {
				b.WriteString("[回复 " + quoted + "]")
			} else {
				b.WriteString("[回复]")
			}
		case TypeRecord:
			b.WriteString("[语音]")
		case TypeVideo:
			b.WriteString("[视频]")
		default:
			// 未知类型（例如表情包 mface）保留类型名，便于排查与扩展。
			b.WriteString(placeholder(seg.Type, seg.Data["summary"]))
		}
	}
	return b.String()
}

func placeholder(name, detail string) string {
	if detail == "" {
		return "[" + name + "]"
	}
	return "[" + name + ":" + detail + "]"
}

// faceLabel 返回表情的可读标签，优先级：平台给出的名称 > 内置编号表 > 原始编号。
//
// 平台对部分表情会在 raw.faceText 里直接给出名称（如 "/大怨种"），那比任何本地表都新；
// 内置表（faces.json）覆盖 QQ 官方编号；两者都没有时退回编号，绝不编造名称。
func faceLabel(seg Segment) string {
	if name := platformFaceText(seg); name != "" {
		return name
	}
	if name, ok := FaceName(seg.Data["id"]); ok {
		return name
	}
	return seg.Data["id"]
}

// platformFaceText 从 face 段的 raw 字段里取平台给出的名称。
func platformFaceText(seg Segment) string {
	raw := strings.TrimSpace(seg.Data["raw"])
	if raw == "" {
		return ""
	}
	var d struct {
		FaceText string `json:"faceText"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d.FaceText), "/"))
}

// imageLabel 区分普通图片与表情包。
//
// 平台对表情包同样上报 image 段，靠这两个字段区分（已在真实平台载荷上核实）：
//   - 表情包：sub_type=1，summary="[动画表情]"
//   - 普通图片：sub_type=0，summary 为空
func imageLabel(seg Segment) string {
	if s := strings.TrimSpace(seg.Data["summary"]); s != "" {
		return s
	}
	if st := strings.TrimSpace(seg.Data["sub_type"]); st != "" && st != "0" {
		return "[动画表情]"
	}
	return "[图片]"
}

// ParseMessage 解析上报的消息，同时支持数组形态与 CQ 码字符串形态。
//
// 非法输入不会 panic：能解析多少解析多少，跳过的片段通过第二个返回值报告。
func ParseMessage(raw json.RawMessage) (Message, []string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return Message{}, nil, nil
	}
	if trimmed[0] == '[' {
		// 逐段解析：某一段坏了只降级成 warning，绝不连累其余段。
		// 否则一个类型不符的字段就会让含文本的整条消息变成空消息。
		var raws []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &raws); err != nil {
			return Message{}, nil, fmt.Errorf("parse message array: %w", err)
		}
		out := make(Message, 0, len(raws))
		var warnings []string
		for i, rb := range raws {
			var s Segment
			if err := json.Unmarshal(rb, &s); err != nil {
				warnings = append(warnings, fmt.Sprintf("segment %d: %v", i, err))
				continue
			}
			out = append(out, normalizeSegment(s))
		}
		return out, warnings, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(trimmed), &s); err != nil {
			return Message{}, nil, fmt.Errorf("parse message string: %w", err)
		}
		return ParseCQString(s)
	}
	return Message{}, nil, fmt.Errorf("unsupported message form: %q", firstRunes(trimmed, 16))
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// UnmarshalJSON 让 data 里的任意 JSON 值都能落进 map[string]string。
//
// 平台对同一字段并不保证类型：image 的 file_size 是数字、face 的 id 可能是数字也可能是
// 字符串。早期直接用 map[string]string 承接，任何一个非字符串值都会让整条消息解析失败。
func (s *Segment) UnmarshalJSON(b []byte) error {
	var raw struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Type = raw.Type
	s.Data = make(map[string]string, len(raw.Data))
	for k, v := range raw.Data {
		s.Data[k] = coerceScalar(v)
	}
	return nil
}

// coerceScalar 把 JSON 标量还原成字符串；null 得到空串，对象/数组退化为其 JSON 文本。
func coerceScalar(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return trimmed
}

func normalizeSegment(s Segment) Segment {
	if s.Data == nil {
		s.Data = map[string]string{}
	}
	return s
}

// ParseCQString 解析 CQ 码字符串形态。
func ParseCQString(s string) (Message, []string, error) {
	var (
		out      Message
		warnings []string
		text     strings.Builder
	)
	flush := func() {
		if text.Len() > 0 {
			out = append(out, Text(unescapeCQ(text.String(), false)))
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "[CQ:") {
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				warnings = append(warnings, "unterminated CQ code at offset "+fmt.Sprint(i))
				text.WriteString(s[i:])
				break
			}
			body := s[i+4 : i+end]
			seg, err := parseCQBody(body)
			if err != nil {
				warnings = append(warnings, err.Error())
				text.WriteString(s[i : i+end+1])
			} else {
				flush()
				out = append(out, seg)
			}
			i += end + 1
			continue
		}
		text.WriteByte(s[i])
		i++
	}
	flush()
	return out, warnings, nil
}

func parseCQBody(body string) (Segment, error) {
	parts := strings.Split(body, ",")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return Segment{}, fmt.Errorf("CQ code with empty type")
	}
	seg := Segment{Type: strings.TrimSpace(parts[0]), Data: map[string]string{}}
	for _, kv := range parts[1:] {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		k, v, found := strings.Cut(kv, "=")
		if !found {
			return Segment{}, fmt.Errorf("CQ code %q has malformed parameter %q", parts[0], kv)
		}
		seg.Data[strings.TrimSpace(k)] = unescapeCQ(v, true)
	}
	return seg, nil
}

func unescapeCQ(s string, inParams bool) string {
	repl := strings.NewReplacer(
		"&#91;", "[",
		"&#93;", "]",
		"&amp;", "&",
	)
	if inParams {
		repl = strings.NewReplacer("&#44;", ",", "&#91;", "[", "&#93;", "]", "&amp;", "&")
	}
	return repl.Replace(s)
}

func escapeCQText(s string) string {
	return strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;").Replace(s)
}

func escapeCQParam(s string) string {
	return strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;", ",", "&#44;").Replace(s)
}

// String 生成 CQ 码字符串形态。
func (m Message) String() string {
	var b strings.Builder
	for _, seg := range m {
		if seg.Type == TypeText {
			b.WriteString(escapeCQText(seg.Data["text"]))
			continue
		}
		b.WriteString("[CQ:")
		b.WriteString(seg.Type)
		keys := make([]string, 0, len(seg.Data))
		for k := range seg.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(",")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(escapeCQParam(seg.Data[k]))
		}
		b.WriteString("]")
	}
	return b.String()
}

// String 打印友好：base64 图片只输出长度与哈希前缀，避免日志爆炸。
func (s Segment) String() string {
	if s.Data == nil {
		return s.Type + "{}"
	}
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := s.Data[k]
		if isBase64ish(v) {
			sum := sha256.Sum256([]byte(v))
			parts = append(parts, k+"=<base64 len="+fmt.Sprint(len(v))+" sha256="+hex.EncodeToString(sum[:4])+">")
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return s.Type + "{" + strings.Join(parts, ",") + "}"
}

func isBase64ish(v string) bool {
	if len(v) < 128 {
		return false
	}
	body := v
	if i := strings.Index(body, "://"); i >= 0 {
		body = body[i+3:]
	}
	if body == "" {
		return false
	}
	head := body
	if len(head) > 64 {
		head = head[:64]
	}
	for _, r := range head {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

// ReplyIDs 返回消息里引用段的被引用消息 id（按出现顺序）。
func (m Message) ReplyIDs() []string {
	var out []string
	for _, seg := range m {
		if seg.Type == TypeReply && seg.Data["id"] != "" {
			out = append(out, seg.Data["id"])
		}
	}
	return out
}

// SetReplyText 把解析出的被引用内容写回对应的引用段（原地修改）。
//
// 就地修改而不是另建结构：Summary()、规则、历史记录都会读同一份 Message，
// 只有一处写、处处可见，才不会出现"日志里有、提示词里没有"这类不一致。
// 返回被填充的段数。
func (m Message) SetReplyText(id, text string) int {
	if id == "" || strings.TrimSpace(text) == "" {
		return 0
	}
	n := 0
	for i := range m {
		if m[i].Type == TypeReply && m[i].Data["id"] == id {
			if m[i].Data == nil {
				m[i].Data = map[string]string{}
			}
			m[i].Data["text"] = strings.TrimSpace(text)
			n++
		}
	}
	return n
}
