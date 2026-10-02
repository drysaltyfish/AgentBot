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
func (m Message) PlainText() string {
	var b strings.Builder
	for _, seg := range m {
		if seg.Type == TypeText {
			b.WriteString(seg.Data["text"])
		}
	}
	return b.String()
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
		var segs []Segment
		if err := json.Unmarshal([]byte(trimmed), &segs); err != nil {
			return Message{}, nil, fmt.Errorf("parse message array: %w", err)
		}
		out := make(Message, 0, len(segs))
		for _, s := range segs {
			out = append(out, normalizeSegment(s))
		}
		return out, nil, nil
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
