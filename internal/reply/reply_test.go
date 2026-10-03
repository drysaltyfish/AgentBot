package reply

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// stubBrain 是可控的 agent.Agent：记录调用次数与输入，返回预设输出。
type stubBrain struct {
	out   *agent.Output
	err   error
	calls int
	onRun func(agent.Input)
}

func (s *stubBrain) Run(ctx context.Context, in agent.Input) (*agent.Output, error) {
	s.calls++
	if s.onRun != nil {
		s.onRun(in)
	}
	if s.out != nil {
		return s.out, s.err
	}
	return &agent.Output{}, s.err
}

// fakeMemory 记录写入；Recall 返回空。
type fakeMemory struct {
	saved []string
}

func (m *fakeMemory) Save(ctx context.Context, text string) error {
	m.saved = append(m.saved, text)
	return nil
}

func (m *fakeMemory) Recall(ctx context.Context) ([]string, error) { return nil, nil }

// recordingCaller 记录发往平台的调用。
type recordingCaller struct {
	mu   sync.Mutex
	reqs []transport.Request
}

func (c *recordingCaller) Call(ctx context.Context, req transport.Request) (transport.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return transport.Response{RetCode: 0, Data: json.RawMessage(`{"message_id":1}`)}, nil
}

func (c *recordingCaller) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func testLogger(t *testing.T) *observe.Logger {
	t.Helper()
	lg := observe.New(observe.Options{Level: "error", Format: "json", QueueSize: 64, Writer: io.Discard})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	})
	return lg
}

func testSessions(t *testing.T) *session.Manager {
	t.Helper()
	mgr := session.New(
		session.WithHistory(history.NewMemory(100)),
		session.WithTTL(time.Hour),
		session.WithMax(16),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	})
	return mgr
}

func newTestPipeline(t *testing.T, brain agent.Agent, mgr *session.Manager, lg *observe.Logger) *Pipeline {
	t.Helper()
	return New(Deps{Brain: brain, Sessions: mgr, Log: lg, Timeout: 5 * time.Second})
}

func testJob(key session.Key) Job {
	return Job{Key: key, GroupID: key.GroupID, UserID: key.UserID, Text: "你好", TraceID: "t1", ShouldReply: true}
}

// Test_TurnIsRecordedEvenWhenNotReplying 钉住"先记录、后决定是否回复"：
// 群里的环境消息不回复，但必须成为后续对话的上下文。
func Test_TurnIsRecordedEvenWhenNotReplying(t *testing.T) {
	t.Parallel()
	lg := testLogger(t)
	mgr := testSessions(t)
	brain := &stubBrain{}
	p := newTestPipeline(t, brain, mgr, lg)
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}

	job := testJob(key)
	job.ShouldReply = false
	p.Handle(context.Background(), job)

	if brain.calls != 0 {
		t.Fatalf("不回复的消息不应调用模型: calls=%d", brain.calls)
	}
	sess, ok := mgr.Get(key)
	if !ok {
		t.Fatalf("会话未创建")
	}
	items, err := sess.Hist.Messages(context.Background(), key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 1 || items[0].Kind != history.KindUser || items[0].Content != "你好" {
		t.Fatalf("环境消息未被记录: %+v", items)
	}
	if items[0].Ambient != true {
		t.Fatalf("未回复的消息应标记为环境消息: %+v", items[0])
	}
}

// Test_EndOfTurnSendsNothingButKeepsHistory 钉住 end_action 的控制流：
// 本轮不发消息，但用户轮次仍然留在历史里。
func Test_EndOfTurnSendsNothingButKeepsHistory(t *testing.T) {
	t.Parallel()
	lg := testLogger(t)
	mgr := testSessions(t)
	brain := &stubBrain{out: &agent.Output{}, err: agent.ErrEndOfTurn}
	p := newTestPipeline(t, brain, mgr, lg)
	key := session.Key{SelfID: 1, UserID: 9}

	p.Handle(context.Background(), testJob(key))

	if brain.calls != 1 {
		t.Fatalf("模型应被调用一次: calls=%d", brain.calls)
	}
	sess, _ := mgr.Get(key)
	items, err := sess.Hist.Messages(context.Background(), key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("结束本轮不应写入助手轮次: %+v", items)
	}
}

// Test_AutoMemoryRunsBeforeTheModel 钉住 F-48 的规则触发顺序：
// 显式"记住"必须在跑模型之前落库，这一轮的提示词里才带得上它。
func Test_AutoMemoryRunsBeforeTheModel(t *testing.T) {
	t.Parallel()
	lg := testLogger(t)
	mgr := testSessions(t)
	mem := &fakeMemory{}
	savedBeforeRun := false
	brain := &stubBrain{out: &agent.Output{}, err: agent.ErrEndOfTurn}
	brain.onRun = func(agent.Input) { savedBeforeRun = len(mem.saved) > 0 }
	p := New(Deps{
		Brain: brain, Sessions: mgr, Log: lg, Timeout: 5 * time.Second,
		Memory: mem, AutoMem: agent.NewMemoryCommand([]string{"记住"}),
	})
	key := session.Key{SelfID: 1, UserID: 9}
	job := testJob(key)
	job.Text = "记住：我喜欢橘子味"

	p.Handle(context.Background(), job)

	if len(mem.saved) != 1 || !strings.Contains(mem.saved[0], "我喜欢橘子味") {
		t.Fatalf("显式记忆未写入: %+v", mem.saved)
	}
	if !savedBeforeRun {
		t.Fatalf("记忆必须在模型运行之前写入")
	}
}

// Test_UsageIsRecordedUnderTheSessionKey 钉住 F-85：用量按会话键累加。
func Test_UsageIsRecordedUnderTheSessionKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "reply.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	brain := &stubBrain{out: &agent.Output{
		Usage:    llm.Usage{PromptTokens: 7, CompletionTokens: 3},
		LLMCalls: 1,
	}, err: agent.ErrEndOfTurn}
	p := New(Deps{Brain: brain, Sessions: mgr, Log: lg, Timeout: 5 * time.Second, Store: st})
	key := session.Key{SelfID: 1, UserID: 9}

	p.Handle(ctx, testJob(key))

	tot, err := st.UsageTotals(ctx)
	if err != nil {
		t.Fatalf("UsageTotals: %v", err)
	}
	if tot.Requests != 1 || tot.InputTokens != 7 || tot.OutputTokens != 3 {
		t.Fatalf("用量未按会话累加: %+v", tot)
	}
}

// Test_RepliesGoOutThroughTheSender 钉住"唯一出口"：文本经分段后由 Sender 发出。
func Test_RepliesGoOutThroughTheSender(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubBrain{out: &agent.Output{Text: "第一段\n\n第二段", FinishReason: "stop"}}
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		Shape: Shape{SplitOnBlank: true, MaxSegments: 4},
	})
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}

	p.Handle(ctx, testJob(key))

	if caller.count() == 0 {
		t.Fatalf("回复没有经 Sender 发出")
	}
	sess, _ := mgr.Get(key)
	items, err := sess.Hist.Messages(ctx, key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 2 || items[1].Kind != history.KindAssistant {
		t.Fatalf("助手轮次未追加: %+v", items)
	}
}
