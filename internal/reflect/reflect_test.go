package reflect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/scope"
)

// fakeSource 是确定性的待反思来源。
type fakeSource struct {
	items   map[string][]Item
	saved   []savedFact
	session []string
}

type savedFact struct {
	session string
	subject int64
	text    string
}

func (f *fakeSource) Pending(_ context.Context, key string, since int64, _ int) ([]Item, error) {
	var out []Item
	for _, it := range f.items[key] {
		if it.Seq > since {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeSource) Save(ctx context.Context, session string, subjectID int64, text string) error {
	// 归属必须经 ctx 传进来：无归属 = 公共记忆。
	got := scope.SubjectFrom(ctx)
	if got != subjectID {
		return errors.New("subject mismatch between ctx and argument")
	}
	f.saved = append(f.saved, savedFact{session: session, subject: subjectID, text: text})
	return nil
}

func (f *fakeSource) Sessions() []string { return f.session }

// fakeReflector 返回预置事实并记录被调用的次数与输入。
type fakeReflector struct {
	facts []memory.ReflectedFact
	calls int
	input []string
}

func (f *fakeReflector) Reflect(_ context.Context, entries []string) ([]memory.ReflectedFact, error) {
	f.calls++
	f.input = append([]string(nil), entries...)
	return f.facts, nil
}

func at(min int) time.Time {
	return time.Date(2026, 10, 4, 12, min, 0, 0, time.UTC)
}

// Test_F48_ReflectOnlyWhenIdle 钉住第一道闸门：还在说话就不反思（省钱）。
func Test_F48_ReflectOnlyWhenIdle(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		items:   map[string][]Item{"k": {{Seq: 1, SpeakerID: 100, SpeakerName: "张三", Text: "明天团建"}}},
		session: []string{"k"},
	}
	rf := &fakeReflector{facts: []memory.ReflectedFact{{Fact: "张三很怕辣"}}}
	now := at(0)
	// 上次活动在 1 分钟前：还没安静下来。
	s := New(src, rf, func(string) (time.Time, bool) { return now.Add(-time.Minute), true }, Options{Enabled: true})
	s.now = func() time.Time { return now }

	if n := s.Sweep(context.Background()); n != 0 {
		t.Fatalf("未静默时不应反思: fired=%d", n)
	}
	if rf.calls != 0 {
		t.Fatalf("未静默时不应调用模型: calls=%d", rf.calls)
	}

	// 活动停在 10 分钟前：可以反思了。
	s2 := New(src, rf, func(string) (time.Time, bool) { return now.Add(-10 * time.Minute), true },
		Options{Enabled: true, IdleAfter: 5 * time.Minute})
	s2.now = func() time.Time { return now }
	if n := s2.Sweep(context.Background()); n != 1 {
		t.Fatalf("静默后应反思一次: fired=%d", n)
	}
}

// Test_F48_ReflectThrottledPerSession 钉住第二道闸门：同一会话短时间内不重复反思。
func Test_F48_ReflectThrottledPerSession(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		items:   map[string][]Item{"k": {{Seq: 1, SpeakerID: 100, SpeakerName: "张三", Text: "第一段"}}},
		session: []string{"k"},
	}
	rf := &fakeReflector{facts: []memory.ReflectedFact{{Fact: "张三怕辣"}}}
	now := at(0)
	s := New(src, rf, func(string) (time.Time, bool) { return now.Add(-time.Hour), true },
		Options{Enabled: true, IdleAfter: time.Minute, MinInterval: 30 * time.Minute})
	s.now = func() time.Time { return now }

	if n := s.Sweep(context.Background()); n != 1 {
		t.Fatalf("首轮应反思: fired=%d", n)
	}
	// 立刻再来一轮：应被节流。
	src.items["k"] = append(src.items["k"], Item{Seq: 2, SpeakerID: 100, SpeakerName: "张三", Text: "第二段"})
	if n := s.Sweep(context.Background()); n != 0 {
		t.Fatalf("节流期内不应反思: fired=%d", n)
	}
	if rf.calls != 1 {
		t.Fatalf("模型只应被调用一次: calls=%d", rf.calls)
	}
	// 30 分钟后再来：可以。
	now = now.Add(31 * time.Minute)
	if n := s.Sweep(context.Background()); n != 1 {
		t.Fatalf("超过最小间隔后应反思: fired=%d", n)
	}
}

// Test_F48_WatermarkSkipsNothingNew 钉住第三道闸门：没有新内容就不调用模型。
func Test_F48_WatermarkSkipsNothingNew(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		items:   map[string][]Item{"k": {{Seq: 1, SpeakerID: 100, Text: "只有这一段"}}},
		session: []string{"k"},
	}
	rf := &fakeReflector{facts: nil}
	now := at(0)
	s := New(src, rf, func(string) (time.Time, bool) { return now.Add(-time.Hour), true },
		Options{Enabled: true, MinInterval: time.Second})
	s.now = func() time.Time { return now }

	s.Sweep(context.Background())
	if rf.calls != 1 {
		t.Fatalf("首轮应调用一次: calls=%d", rf.calls)
	}
	if got := s.Watermark("k"); got != 1 {
		t.Fatalf("水位应推进到 1，实际 %d", got)
	}
	// 没有新条目：不再调用。
	now = now.Add(time.Hour)
	if n := s.Sweep(context.Background()); n != 0 {
		t.Fatalf("无新内容时不应反思: fired=%d", n)
	}
	if rf.calls != 1 {
		t.Fatalf("无新内容时不应再调用模型: calls=%d", rf.calls)
	}
}

// Test_F48_SharedAndPersonalFactsAreWrittenWithSubject 钉住归属：公共记忆不带归属，
// 个人记忆归属到被提到的发言人。
func Test_F48_SharedAndPersonalFactsAreWrittenWithSubject(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		items: map[string][]Item{"k": {
			{Seq: 1, SpeakerID: 100, SpeakerName: "张三", Text: "明天团建"},
			{Seq: 2, SpeakerID: 200, SpeakerName: "李四", Text: "我不吃辣"},
		}},
		session: []string{"k"},
	}
	rf := &fakeReflector{facts: []memory.ReflectedFact{
		{Fact: "明天下午三点组织团建", Shared: true},
		{Fact: "李四不吃辣", Shared: false},
	}}
	now := at(0)
	s := New(src, rf, func(string) (time.Time, bool) { return now.Add(-time.Hour), true }, Options{Enabled: true})
	s.now = func() time.Time { return now }

	if n := s.Sweep(context.Background()); n != 1 {
		t.Fatalf("应反思一次: fired=%d", n)
	}
	if len(src.saved) != 2 {
		t.Fatalf("应写入两条，实际 %d: %+v", len(src.saved), src.saved)
	}
	if src.saved[0].subject != 0 {
		t.Fatalf("公共记忆不应有归属: %+v", src.saved[0])
	}
	if src.saved[1].subject != 200 {
		t.Fatalf("「李四不吃辣」应归属李四(200)，实际 %d", src.saved[1].subject)
	}
}

// Test_F48_CompactKeepsCostLow 钉住省钱的两处压缩：连续环境消息合并、单条截断。
func Test_F48_CompactKeepsCostLow(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("很", 400)
	items := []Item{
		{Seq: 1, SpeakerID: 100, SpeakerName: "张三", Ambient: true, Text: "哈"},
		{Seq: 2, SpeakerID: 100, SpeakerName: "张三", Ambient: true, Text: "哈"},
		{Seq: 3, SpeakerID: 100, SpeakerName: "张三", Ambient: true, Text: "哈哈"},
		{Seq: 4, SpeakerID: 200, SpeakerName: "李四", Text: long},
	}
	lines, maxSeq := compact(items)
	if maxSeq != 4 {
		t.Fatalf("maxSeq=%d, want 4", maxSeq)
	}
	if len(lines) != 2 {
		t.Fatalf("连续环境消息应合并为一条，实际 %d 行: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "连发 3 条") {
		t.Fatalf("合并行应写明条数: %q", lines[0])
	}
	if len([]rune(lines[1])) > 200 {
		t.Fatalf("单条应被截断: %d rune", len([]rune(lines[1])))
	}
}

// Test_F48_ReflectFailureKeepsWatermark 钉住失败语义：失败不推进水位，下轮重试。
func Test_F48_ReflectFailureKeepsWatermark(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		items:   map[string][]Item{"k": {{Seq: 7, SpeakerID: 100, Text: "内容"}}},
		session: []string{"k"},
	}
	rf := &failReflector{}
	now := at(0)
	s := New(src, rf, func(string) (time.Time, bool) { return now.Add(-time.Hour), true },
		Options{Enabled: true, MinInterval: time.Second})
	s.now = func() time.Time { return now }

	if n := s.Sweep(context.Background()); n != 0 {
		t.Fatalf("失败不应计入已反思: fired=%d", n)
	}
	if got := s.Watermark("k"); got != 0 {
		t.Fatalf("失败时水位不应推进，实际 %d", got)
	}
}

type failReflector struct{}

func (failReflector) Reflect(context.Context, []string) ([]memory.ReflectedFact, error) {
	return nil, errors.New("boom")
}

// Test_F48_ParseFactsTolerantButStrict 覆盖输出解析：容忍代码块包裹，拒绝自由文本。
func Test_F48_ParseFactsTolerantButStrict(t *testing.T) {
	t.Parallel()
	got, err := memory.ParseFacts("好的，结果如下：\n[{\"fact\":\"张三怕辣\",\"shared\":false},{\"fact\":\"明天团建\",\"shared\":true}]\n以上。", 5, 60)
	if err != nil {
		t.Fatalf("应容忍前后解释: %v", err)
	}
	if len(got) != 2 || got[0].Fact != "张三怕辣" || !got[1].Shared {
		t.Fatalf("解析结果不对: %+v", got)
	}
	// 自由文本必须被拒绝——自动写入的内容不允许无法解释。
	if _, err := memory.ParseFacts("张三很怕辣", 5, 60); !errors.Is(err, memory.ErrReflectUnparsed) {
		t.Fatalf("非 JSON 应报 ErrReflectUnparsed，实际 %v", err)
	}
	// 上限与去重。
	dup, err := memory.ParseFacts("[{\"fact\":\"a\"},{\"fact\":\"a\"},{\"fact\":\"b\"},{\"fact\":\"c\"}]", 2, 60)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(dup) != 2 {
		t.Fatalf("应去重并截到 maxFacts=2，实际 %d: %+v", len(dup), dup)
	}
}
