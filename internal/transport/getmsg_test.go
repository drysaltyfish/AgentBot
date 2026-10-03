package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type getMsgCaller struct {
	response Response
	err      error
	lastReq  Request
}

func (g *getMsgCaller) Call(ctx context.Context, req Request) (Response, error) {
	g.lastReq = req
	return g.response, g.err
}

func Test_F84_GetMsgTextExtractsSegments(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(map[string]any{
		"message_id": 42,
		"message": []map[string]any{
			{"type": "reply", "data": map[string]any{"id": "1"}},
			{"type": "text", "data": map[string]any{"text": "这就是我说的那句"}},
		},
	})
	c := &getMsgCaller{response: Response{RetCode: 0, Data: data}}

	text, err := GetMsgText(context.Background(), c, "42")
	if err != nil {
		t.Fatalf("GetMsgText: %v", err)
	}
	if c.lastReq.Action != "get_msg" {
		t.Fatalf("应调用 get_msg: %q", c.lastReq.Action)
	}
	if c.lastReq.Params["message_id"] != "42" {
		t.Fatalf("应传 message_id: %+v", c.lastReq.Params)
	}
	// 被引用的那条消息本身也含引用段，因此会渲染出占位符——
	// 关键是要抽出**正文**，而不是只得到占位符。
	if !strings.Contains(text, "这就是我说的那句") {
		t.Fatalf("应抽出正文: %q", text)
	}
}

// Test_F84_GetMsgTextHandlesPlainStringMessage 覆盖平台把 message 发成纯字符串的情况。
func Test_F84_GetMsgTextHandlesPlainStringMessage(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(map[string]any{"message": "纯字符串正文"})
	c := &getMsgCaller{response: Response{RetCode: 0, Data: data}}
	text, err := GetMsgText(context.Background(), c, "1")
	if err != nil {
		t.Fatalf("GetMsgText: %v", err)
	}
	if text != "纯字符串正文" {
		t.Fatalf("应兼容字符串形态: %q", text)
	}
}

func Test_F84_GetMsgTextRejectsBadInput(t *testing.T) {
	t.Parallel()
	c := &getMsgCaller{}
	if _, err := GetMsgText(context.Background(), c, "  "); err == nil {
		t.Fatalf("空 id 必须报错")
	}
	bad := &getMsgCaller{response: Response{RetCode: 1404, Message: "msg not found"}}
	if _, err := GetMsgText(context.Background(), bad, "9"); err == nil {
		t.Fatalf("非零 retcode 必须报错")
	}
}
