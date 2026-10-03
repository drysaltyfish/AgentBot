package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// streamClock 是可注入的确定性时钟。
type streamClock struct{ t time.Time }

func (c *streamClock) Now() time.Time          { return c.t }
func (c *streamClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// flushRecorder 记录所有发送事件。
type flushRecorder struct{ events []StreamFlush }

func (r *flushRecorder) fn() StreamFlushFunc {
	return func(f StreamFlush) { r.events = append(r.events, f) }
}

func (r *flushRecorder) concat() string {
	var b strings.Builder
	for _, e := range r.events {
		if e.Kind == FlushDelta || e.Kind == FlushFinal {
			b.WriteString(e.Delta)
		}
	}
	return b.String()
}

func newTestSplitter(t *testing.T, clock *streamClock, rec *flushRecorder, mutate func(*StreamConfig)) *StreamSplitter {
	t.Helper()
	cfg := StreamConfig{Now: clock.Now, Flush: rec.fn()}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewStreamSplitter(cfg)
}

func Test_F64_TriggersOnSentenceBoundary(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta("你好。")

	if len(rec.events) != 1 {
		t.Fatalf("句末标点应立即触发一次发送: %+v", rec.events)
	}
	f := rec.events[0]
	if f.Kind != FlushDelta || f.Delta != "你好。" || !f.First {
		t.Fatalf("首帧内容错误: %+v", f)
	}
}

func Test_F64_TriggersOnLengthThreshold(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta(strings.Repeat("a", DefaultFlushChars))

	if len(rec.events) != 1 {
		t.Fatalf("达到长度阈值应触发: %+v", rec.events)
	}
	if rec.events[0].Delta != strings.Repeat("a", DefaultFlushChars) {
		t.Fatalf("长度触发内容错误: %q", rec.events[0].Delta)
	}
}

func Test_F64_TriggersOnTimeThresholdViaTick(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, func(c *StreamConfig) { c.FirstMinChars = 1 })
	s.FeedDelta("hello")
	if len(rec.events) != 0 {
		t.Fatalf("时间未到不应发送: %+v", rec.events)
	}
	clock.Advance(DefaultFlushInterval)
	s.Tick()
	if len(rec.events) != 1 || rec.events[0].Delta != "hello" {
		t.Fatalf("时间阈值应触发: %+v", rec.events)
	}
}

func Test_F64_RateLimitCoalesces(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta("aa.")
	if len(rec.events) != 1 {
		t.Fatalf("首帧应发出: %+v", rec.events)
	}
	// 频率上限内不得再发。
	s.FeedDelta("bb.")
	if len(rec.events) != 1 {
		t.Fatalf("限流期内不应发送: %+v", rec.events)
	}
	clock.Advance(400 * time.Millisecond)
	s.Tick()
	if len(rec.events) != 1 {
		t.Fatalf("未到 MinInterval 仍不应发送: %+v", rec.events)
	}
	clock.Advance(400 * time.Millisecond)
	s.Tick()
	if len(rec.events) != 2 {
		t.Fatalf("限流结束后应合并发送一次: %+v", rec.events)
	}
	if rec.events[1].Delta != "bb." {
		t.Fatalf("合并内容错误: %q", rec.events[1].Delta)
	}
}

func Test_F64_FlushesRemainderOnFinish(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta("没有句末标点的尾巴")
	if len(rec.events) != 0 {
		t.Fatalf("未触发时不应发送: %+v", rec.events)
	}
	s.Finish()
	if len(rec.events) != 1 || rec.events[0].Kind != FlushFinal {
		t.Fatalf("流结束应发出剩余: %+v", rec.events)
	}
	if got := rec.concat(); got != "没有句末标点的尾巴" {
		t.Fatalf("剩余内容丢失: %q", got)
	}
	if s.Pending() != "" {
		t.Fatalf("结束后不应再有缓冲")
	}
}

func Test_F64_DoesNotSplitInsideCodeFence(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	triple := "\u0060\u0060\u0060"
	for _, part := range []string{triple, "a.b\n", "c", triple, "end."} {
		s.FeedDelta(part)
	}
	if len(rec.events) != 1 {
		t.Fatalf("代码围栏内不应切分，围栏关闭后才发一次: %+v", rec.events)
	}
	want := triple + "a.b\nc" + triple + "end."
	if rec.events[0].Delta != want {
		t.Fatalf("围栏内容被切坏: %q", rec.events[0].Delta)
	}
}

func Test_F64_RewriteReplaceWhenEditable(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, func(c *StreamConfig) { c.EditMessages = true })
	s.FeedDelta("hello")
	s.FeedFull("hello world.")
	if len(rec.events) != 1 || rec.events[0].Delta != "hello world." {
		t.Fatalf("扩展快照应按增量发送: %+v", rec.events)
	}
	s.FeedFull("HELLO WORLD!")
	if len(rec.events) != 2 || rec.events[1].Kind != FlushReplace {
		t.Fatalf("支持编辑时应发送整条替换: %+v", rec.events)
	}
	if rec.events[1].Full != "HELLO WORLD!" {
		t.Fatalf("替换内容错误: %q", rec.events[1].Full)
	}
	if s.Halted() {
		t.Fatalf("支持编辑时不应中止")
	}
}

func Test_F64_RewriteHaltsWhenAppendOnly(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta("abc.")
	if len(rec.events) != 1 {
		t.Fatalf("首帧应发出: %+v", rec.events)
	}
	s.FeedFull("XYZ.")
	if !s.Halted() {
		t.Fatalf("追加式平台遇到重写应中止")
	}
	if len(rec.events) != 2 || rec.events[1].Kind != FlushHalt {
		t.Fatalf("应发出中止事件: %+v", rec.events)
	}
	// 中止后不得再有任何内容被发出。
	s.FeedDelta("more")
	s.Finish()
	if len(rec.events) != 2 {
		t.Fatalf("中止后不应继续发送: %+v", rec.events)
	}
}

func Test_F64_ErrorInterruptsWithoutRollback(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	s.FeedDelta("abc.")
	s.FeedDelta("de")
	s.Feed(Chunk{Err: errors.New("boom")})

	if len(rec.events) != 2 {
		t.Fatalf("应有一次增量 + 一次中断收尾: %+v", rec.events)
	}
	last := rec.events[1]
	if last.Kind != FlushFinal || !last.Interrupted {
		t.Fatalf("末尾应标记中断: %+v", last)
	}
	if got := rec.concat(); got != "abc.de" {
		t.Fatalf("已发送内容不得回滚: %q", got)
	}
}

func Test_F64_ConsumeStreamFinishesOnClose(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)
	ch := make(chan Chunk, 4)
	ch <- Chunk{Content: "hi"}
	ch <- Chunk{Content: " there"}
	close(ch)
	s.ConsumeStream(context.Background(), ch)
	if got := rec.concat(); got != "hi there" {
		t.Fatalf("流关闭时应补发剩余: %q", got)
	}
}

func Test_F64_AcceptanceManyChunks(t *testing.T) {
	t.Parallel()
	clock := &streamClock{t: time.Unix(0, 0)}
	rec := &flushRecorder{}
	s := newTestSplitter(t, clock, rec, nil)

	const chunks = 200
	var want strings.Builder
	for i := 0; i < chunks; i++ {
		part := "第" + itoa(i) + "句。"
		want.WriteString(part)
		s.Feed(Chunk{Content: part})
		clock.Advance(10 * time.Millisecond)
	}
	s.Finish()

	if got := rec.concat(); got != want.String() {
		t.Fatalf("最终拼接必须等于完整回答: len(actual)=%d len(expected)=%d", len(got), want.Len())
	}
	elapsed := 10 * time.Millisecond * chunks
	maxSends := int(elapsed/DefaultFlushInterval) + 2
	if len(rec.events) > maxSends {
		t.Fatalf("发送次数超过限流上限: actual=%d max=%d", len(rec.events), maxSends)
	}
	// 流结束时不得丢尾。
	if s.Pending() != "" {
		t.Fatalf("结束后不得有剩余缓冲: %q", s.Pending())
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
