// Package event 实现双轨事件模型、通用消息 ID 与消息段（FEATURES.md F-01/F-02/F-03）。
//
// 双轨：业务用归一字段（Kind/Sub/SelfID/...），扩展用原始包（Raw）。
package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxRawSize 是原始包的大小上限；超过即截断并置 RawTruncated。
const MaxRawSize = 1 << 20

// Kind 是事件大类。
type Kind string

// 事件大类常量。
const (
	KindMessage Kind = "message"
	KindNotice  Kind = "notice"
	KindRequest Kind = "request"
	KindMeta    Kind = "meta"
)

// Sender 是发送者的归一字段。
type Sender struct {
	UserID   int64
	Nickname string
	Card     string
	Role     string
}

// Event 是一次上报事件。
type Event struct {
	Kind    Kind
	Sub     string
	SubSub  string
	SelfID  int64
	UserID  int64
	GroupID int64

	MessageID ID
	Time      time.Time
	Sender    Sender
	Message   Message

	// Raw 完整保存平台上报的 JSON（受 MaxRawSize 约束）。
	Raw          json.RawMessage
	RawTruncated bool
	// DecodeWarning 记录一次解码告警；空串表示一切正常。
	DecodeWarning string

	once sync.Once
	doc  map[string]any
}

// NewEvent 用原始 JSON 构造事件。永不 panic，也永不返回 nil。
func NewEvent(raw json.RawMessage) *Event {
	e := &Event{}
	if len(raw) == 0 {
		return e
	}
	if len(raw) > MaxRawSize {
		e.RawTruncated = true
		raw = raw[:MaxRawSize]
	}
	// 必须拷贝：绝不能零拷贝引用可能被复用的网络缓冲。
	e.Raw = append(json.RawMessage(nil), raw...)
	e.normalize()
	return e
}

func (e *Event) normalize() {
	doc, err := decodeObject(e.Raw)
	if err != nil {
		e.DecodeWarning = "invalid raw json: " + err.Error()
		return
	}
	e.doc = doc

	post, _ := doc["post_type"].(string)
	e.Kind = kindOf(post)
	e.Sub = subOf(e.Kind, doc)
	e.SubSub = stringField(doc, "sub_type")

	e.SelfID = intField(doc, "self_id")
	e.UserID = intField(doc, "user_id")
	e.GroupID = intField(doc, "group_id")
	e.MessageID = idField(doc, "message_id")
	e.Time = timeField(doc, "time")

	if sender, ok := doc["sender"].(map[string]any); ok {
		e.Sender = Sender{
			UserID:   intField(sender, "user_id"),
			Nickname: stringField(sender, "nickname"),
			Card:     stringField(sender, "card"),
			Role:     stringField(sender, "role"),
		}
	}
	if e.UserID == 0 {
		e.UserID = e.Sender.UserID
	}
	if e.Kind == KindMessage {
		if msg, ok := doc["message"]; ok {
			if b, err := json.Marshal(msg); err == nil {
				parsed, warns, perr := ParseMessage(b)
				switch {
				case perr != nil:
					if e.DecodeWarning == "" {
						e.DecodeWarning = "parse message: " + perr.Error()
					}
				default:
					e.Message = parsed
					if len(warns) > 0 && e.DecodeWarning == "" {
						e.DecodeWarning = "message: " + strings.Join(warns, "; ")
					}
				}
			}
		}
	}
}

func kindOf(post string) Kind {
	switch post {
	case "message", "message_sent":
		return KindMessage
	case "notice":
		return KindNotice
	case "request":
		return KindRequest
	case "meta_event":
		return KindMeta
	default:
		return Kind("")
	}
}

func subOf(kind Kind, doc map[string]any) string {
	switch kind {
	case KindMessage:
		return stringField(doc, "message_type")
	case KindNotice:
		return stringField(doc, "notice_type")
	case KindRequest:
		return stringField(doc, "request_type")
	case KindMeta:
		return stringField(doc, "meta_event_type")
	default:
		return ""
	}
}

// Get 用点分路径（如 "sender.card"）从原始包中按需取值。
//
// Raw 为空、非法或被截断时返回 (nil, false)，不 panic。
func (e *Event) Get(path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	e.once.Do(func() {
		if e.doc != nil {
			return
		}
		if len(e.Raw) == 0 {
			return
		}
		doc, err := decodeObject(e.Raw)
		if err != nil {
			if e.DecodeWarning == "" {
				e.DecodeWarning = "invalid raw json: " + err.Error()
			}
			return
		}
		e.doc = doc
	})
	if e.doc == nil {
		return nil, false
	}
	var cur any = e.doc
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Warning 返回解码告警（若有）。
func (e *Event) Warning() string { return e.DecodeWarning }

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("raw json is %T, want object", v)
	}
	return doc, nil
}

func stringField(doc map[string]any, key string) string {
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}

func intField(doc map[string]any, key string) int64 {
	v, ok := doc[key]
	if !ok {
		return 0
	}
	return toInt64(v)
}

func idField(doc map[string]any, key string) ID {
	v, ok := doc[key]
	if !ok {
		return ID{}
	}
	switch t := v.(type) {
	case string:
		return IDFromString(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return IDFromInt64(n)
		}
		return IDFromString(t.String())
	case float64:
		return IDFromInt64(int64(t))
	default:
		return ID{}
	}
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

func timeField(doc map[string]any, key string) time.Time {
	sec := intField(doc, key)
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// Fields 返回原始包的顶层键（便于调试与测试）。
func (e *Event) Fields() map[string]any {
	_, _ = e.Get("")
	return e.doc
}
