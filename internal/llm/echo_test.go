package llm

import (
	"context"
	"strings"
	"testing"
)

func Test_F26_EchoRepliesWithTemplate(t *testing.T) {
	t.Parallel()
	e := NewEcho("")
	req := &ChatRequest{Messages: []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "第一条"},
		{Role: RoleAssistant, Content: "回复"},
		{Role: RoleUser, Content: "  你好  "},
	}}
	resp, err := e.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	want := DefaultEchoPrefix + "你好"
	if resp.Content != want {
		t.Fatalf("content: actual=%q expected=%q", resp.Content, want)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish reason: actual=%q", resp.FinishReason)
	}
	if resp.Usage.TotalTokens == 0 {
		t.Fatalf("usage should be filled")
	}
}

func Test_F26_EchoHonoursPrefixAndValidation(t *testing.T) {
	t.Parallel()
	e := NewEcho("回声：")
	resp, err := e.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "回声：x" {
		t.Fatalf("custom prefix: actual=%q", resp.Content)
	}
	if _, err := e.Chat(context.Background(), &ChatRequest{}); err == nil {
		t.Fatalf("empty messages should be rejected")
	}
}

func Test_F28_EchoStreamEndsWithDoneChunk(t *testing.T) {
	t.Parallel()
	e := NewEcho("")
	ch, err := e.ChatStream(context.Background(), &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var content string
	var done bool
	for c := range ch {
		content += c.Content
		if c.Done {
			done = true
		}
	}
	if !done {
		t.Fatalf("stream did not emit a terminal chunk")
	}
	if !strings.Contains(content, "hi") {
		t.Fatalf("streamed content: %q", content)
	}
}

func Test_F26_LastUserMessagePicksTheLatest(t *testing.T) {
	t.Parallel()
	if got := LastUserMessage(nil); got != "" {
		t.Fatalf("nil request: actual=%q", got)
	}
	req := &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "a"}, {Role: RoleUser, Content: "b"}}}
	if got := LastUserMessage(req); got != "b" {
		t.Fatalf("actual=%q expected=b", got)
	}
}
