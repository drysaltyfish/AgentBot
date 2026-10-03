package outbound

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

type streamTestClock struct{ t time.Time }

func (c *streamTestClock) Now() time.Time { return c.t }

// rawChain 关闭全部过滤，便于逐条断言增量原文。
func rawChain() *Chain {
	c := New()
	for _, name := range Order {
		c.Enable(name, false)
	}
	return c
}

func sentTexts(t *testing.T, caller *recordingCaller) []string {
	t.Helper()
	caller.mu.Lock()
	defer caller.mu.Unlock()
	out := make([]string, 0, len(caller.reqs))
	for _, req := range caller.reqs {
		out = append(out, sentMessage(t, req).PlainText())
	}
	return out
}

func Test_F64_StreamSenderSendsDeltas(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	ss := NewStreamSender(NewSender(caller, rawChain()), GroupTarget(9), StreamOptions{})
	ctx := context.Background()

	ss.OnFlush(ctx, llm.StreamFlush{Kind: llm.FlushDelta, Delta: "你好。", First: true})
	ss.OnFlush(ctx, llm.StreamFlush{Kind: llm.FlushFinal, Delta: "世界。", First: false})

	if got := sentTexts(t, caller); strings.Join(got, "") != "你好。世界。" {
		t.Fatalf("增量发送内容错误: %q", got)
	}
	if ss.Sends() != 2 || ss.Halted() {
		t.Fatalf("发送计数/状态错误: sends=%d halted=%v", ss.Sends(), ss.Halted())
	}
}

func Test_F64_StreamSenderTypingHintOnce(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	ss := NewStreamSender(NewSender(caller, rawChain()), GroupTarget(9), StreamOptions{TypingHint: "正在输入…"})
	ctx := context.Background()

	ss.OnFlush(ctx, llm.StreamFlush{Kind: llm.FlushDelta, Delta: "甲", First: true})
	ss.OnFlush(ctx, llm.StreamFlush{Kind: llm.FlushDelta, Delta: "乙", First: false})

	got := sentTexts(t, caller)
	if len(got) != 3 || got[0] != "正在输入…" || got[1] != "甲" || got[2] != "乙" {
		t.Fatalf("首帧提示应只发一次: %q", got)
	}
}

func Test_F64_StreamSenderHaltsOnReplaceWithoutEdit(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	ss := NewStreamSender(NewSender(caller, rawChain()), GroupTarget(9), StreamOptions{})
	ss.OnFlush(context.Background(), llm.StreamFlush{Kind: llm.FlushDelta, Delta: "abc.", First: true})
	ss.OnFlush(context.Background(), llm.StreamFlush{Kind: llm.FlushReplace, Full: "XYZ."})

	if !ss.Halted() || !errors.Is(ss.HaltErr(), ErrStreamHalted) {
		t.Fatalf("追加式平台遇替换应中止: halted=%v err=%v", ss.Halted(), ss.HaltErr())
	}
	if caller.count() != 1 {
		t.Fatalf("中止后不应再发送: %d", caller.count())
	}
	// 中止后后续事件被忽略。
	ss.OnFlush(context.Background(), llm.StreamFlush{Kind: llm.FlushDelta, Delta: "tail", First: false})
	if caller.count() != 1 {
		t.Fatalf("中止后事件应被忽略: %d", caller.count())
	}
}

func Test_F64_StreamSenderEditsWhenSupported(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	var edited []string
	ss := NewStreamSender(NewSender(caller, rawChain()), GroupTarget(9), StreamOptions{
		EditMessages: true,
		Edit: func(ctx context.Context, target Target, full string) error {
			edited = append(edited, full)
			return nil
		},
	})
	ss.OnFlush(context.Background(), llm.StreamFlush{Kind: llm.FlushReplace, Full: "XYZ."})
	if len(edited) != 1 || edited[0] != "XYZ." {
		t.Fatalf("支持编辑时应调用 Edit: %q", edited)
	}
	if caller.count() != 0 || ss.Halted() {
		t.Fatalf("编辑不应触发新消息或中止: count=%d halted=%v", caller.count(), ss.Halted())
	}
}

func Test_F64_StreamSenderWiresToSplitter(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	ss := NewStreamSender(NewSender(caller, rawChain()), GroupTarget(9), StreamOptions{})
	clock := &streamTestClock{t: time.Unix(0, 0)}
	split := llm.NewStreamSplitter(llm.StreamConfig{Now: clock.Now, Flush: ss.Handler(context.Background())})

	split.FeedDelta("你好。")
	clock.t = clock.t.Add(time.Second)
	split.FeedDelta("世界。")
	split.Finish()

	got := sentTexts(t, caller)
	if strings.Join(got, "") != "你好。世界。" {
		t.Fatalf("端到端增量拼接错误: %q", got)
	}
}
