package reply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/semcache"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
	"github.com/drysaltyfish/agentbot/internal/vector"
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

// Test_DeliverIsTheSingleSendingPath 钉住"投递只有一份实现"。
//
// 正常回复与语义缓存命中此前各写一遍分段+发送+记账，差别只有日志措辞。
// 这份重复与"所有出站消息经同一出口"直接矛盾：改了分段方式或记账，
// 很容易只改到其中一处，而另一处只在缓存命中时才被走到。
func Test_DeliverIsTheSingleSendingPath(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	p := &Pipeline{deps: Deps{
		Sender:  outbound.NewSender(caller, outbound.New()),
		Catalog: cat,
		Shape:   Shape{SplitOnBlank: true, MaxSegments: 4, Delay: 0},
	}}

	target := outbound.PrivateTarget(42)
	segments, ok := p.deliver(context.Background(), target, "第一段\n\n第二段\n\n第三段", testSlog())
	if !ok {
		t.Fatal("正常文本应当投递成功")
	}
	if segments != 3 {
		t.Fatalf("按空行应拆成 3 段，实际 %d", segments)
	}
	if got := caller.count(); got != 3 {
		t.Fatalf("应当发出 3 条消息，实际 %d", got)
	}
	if got := actionsSentFor(t, cat, "send_msg"); got != 3 {
		t.Fatalf("ActionsSent 应记 3 次，实际 %v", got)
	}
}

// Test_DeliverReportsFailureWithoutCountingSuccess 发送失败时不得记账。
//
// "计一次成功"与"真的发出去"必须一致，否则指标会掩盖发送失败。
func Test_DeliverReportsFailureWithoutCountingSuccess(t *testing.T) {
	t.Parallel()
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	p := &Pipeline{deps: Deps{
		Sender:  outbound.NewSender(failingCaller{}, outbound.New()),
		Catalog: cat,
		Shape:   Shape{SplitOnBlank: false, MaxSegments: 4},
	}}

	if _, ok := p.deliver(context.Background(), outbound.PrivateTarget(42), "你好", testSlog()); ok {
		t.Fatal("发送失败时 deliver 必须返回 false")
	}
	if got := actionsSentFor(t, cat, "send_msg"); got != 0 {
		t.Fatalf("失败的发送不该记成功，实际 %v", got)
	}
}

// failingCaller 让每一次平台调用都失败。
type failingCaller struct{}

func (failingCaller) Call(context.Context, transport.Request) (transport.Response, error) {
	return transport.Response{}, errors.New("平台不可用")
}

// testSlog 返回一个丢弃输出的 slog，用于直接驱动需要 *slog.Logger 的步骤。
func testSlog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// actionsSentFor 从 Prometheus 文本里读某个 action 的发送计数。
func actionsSentFor(t *testing.T, cat *metrics.Catalog, action string) float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := cat.Registry.WritePrometheus(&buf); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	want := `action="` + action + `"`
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "actions_sent_total{") || !strings.Contains(line, want) {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("解析指标值失败: %q", line)
		}
		return v
	}
	return 0
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

// Test_F66_QuotaDenialRepliesWithANotice 覆盖 F-66 的「deny：拒绝请求并回复提示」。
//
// 静默失败会让用户以为消息丢了，再发一次——正好又撞一次限。因此拒绝必须可见。
// 同时断言：被拒绝的轮次不追加助手回复，也不该污染后续对话上下文。
func Test_F66_QuotaDenialRepliesWithANotice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubBrain{err: fmt.Errorf("llm call denied by cost quota: %w",
		&cost.QuotaError{Scope: cost.ScopeSession, Period: cost.PeriodDay, Limit: 1, Used: 1})}
	p := New(Deps{Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second})
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}

	p.Handle(ctx, testJob(key))

	if caller.count() == 0 {
		t.Fatal("配额拒绝必须回复提示，而不是静默失败")
	}
	sess, ok := mgr.Get(key)
	if !ok {
		t.Fatal("会话未建立")
	}
	items, err := sess.Hist.Messages(ctx, key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 1 || items[0].Kind != history.KindUser {
		t.Fatalf("被拒绝的轮次不得追加助手回复: %+v", items)
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

// Test_F63_SecondIdenticalQuestionSkipsTheModel 是 F-63 的核心验收：
// 同一问题第二次直接命中（Brain 调用不增加）；换人格指纹则必须重新调用。
func Test_F63_SecondIdenticalQuestionSkipsTheModel(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubBrain{out: &agent.Output{Text: "固定回答", FinishReason: "stop"}}
	cache, err := semcache.New(semcache.Options{
		Vectorize: func(q string) vector.Binary { return vector.TextBinary(q, vector.TextDim) },
	})
	if err != nil {
		t.Fatalf("semcache.New: %v", err)
	}
	persona := "persona-a"
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		Semcache:            cache,
		SemcacheFingerprint: func(context.Context, session.Key) string { return persona },
	})
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}
	job := testJob(key)
	job.Text = "你是谁"

	p.Handle(ctx, job)
	if brain.calls != 1 {
		t.Fatalf("首次应调用模型，实际 %d 次", brain.calls)
	}
	p.Handle(ctx, job)
	if brain.calls != 1 {
		t.Fatalf("第二次相同问题应命中缓存，模型调用=%d", brain.calls)
	}
	if stats := cache.Stats(); stats.Hits != 1 {
		t.Fatalf("命中计数=%d，期望 1", stats.Hits)
	}
	if caller.count() < 2 {
		t.Fatal("命中也要把回答发出去（走同一条出口）")
	}
	sess, ok := mgr.Get(key)
	if !ok {
		t.Fatal("会话未建立")
	}
	items, err := sess.Hist.Messages(ctx, key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	// 两次交互 = 2 条用户轮 + 2 条助手轮；缓存命中不得漏记助手轮。
	if len(items) != 4 {
		t.Fatalf("历史条目=%d，期望 4: %+v", len(items), items)
	}

	// 换人格：同一问题不得复用旧人格的答案。
	persona = "persona-b"
	p.Handle(ctx, job)
	if brain.calls != 2 {
		t.Fatalf("换人格后应重新调用模型，实际 %d 次", brain.calls)
	}
}

// Test_F63_ToolTurnsAreNotCached 钉住安全边界：带工具调用的轮次不进缓存。
func Test_F63_ToolTurnsAreNotCached(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubBrain{out: &agent.Output{
		Text:      "用了工具的回答",
		ToolCalls: []llm.ToolCall{{ID: "1", Name: "recall_history"}},
	}}
	cache, err := semcache.New(semcache.Options{
		Vectorize: func(q string) vector.Binary { return vector.TextBinary(q, vector.TextDim) },
	})
	if err != nil {
		t.Fatalf("semcache.New: %v", err)
	}
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		Semcache: cache, SemcacheFingerprint: func(context.Context, session.Key) string { return "p" },
	})
	job := testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3})
	job.Text = "上次我们说了什么"
	p.Handle(ctx, job)
	if got := cache.Len(); got != 0 {
		t.Fatalf("带工具调用的轮次不应进缓存，条目=%d", got)
	}
}

// Test_F63_CacheHitGoesThroughTheSendChain 钉住出口过滤：命中的回答也必须走 Sender。
func Test_F63_CacheHitGoesThroughTheSendChain(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	// 注意：filter 名字必须是 `outbound.Order` 里的已知位置。此前这里注册的是 "suffix"，
	// 而 `Chain.Apply` 只按 Order 遍历已知位置——那个 filter 从未执行过，
	// 测试却一直是绿的。真正断言"内容被处理过"的是 Test_F55_*（见 exit_filter_test.go）。
	chain := outbound.New(outbound.WithFilter(outbound.FilterNormalize, func(s string) string { return s + "-filtered" }))
	sender := outbound.NewSender(caller, chain)
	brain := &stubBrain{out: &agent.Output{Text: "答案", FinishReason: "stop"}}
	cache, err := semcache.New(semcache.Options{
		Vectorize: func(q string) vector.Binary { return vector.TextBinary(q, vector.TextDim) },
	})
	if err != nil {
		t.Fatalf("semcache.New: %v", err)
	}
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		Semcache: cache, SemcacheFingerprint: func(context.Context, session.Key) string { return "p" },
	})
	job := testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3})
	job.Text = "同一个问题"
	p.Handle(ctx, job)
	sentBefore := caller.count()
	p.Handle(ctx, job)
	if caller.count() <= sentBefore {
		t.Fatal("缓存命中必须经 Sender 发出（出口过滤链是唯一出口）")
	}
}

// stubStreamBrain 同时实现 Agent 与 StreamingAgent，用于验证回复链路的分支选择。
type stubStreamBrain struct {
	out    *agent.Output
	chunks []llm.Chunk
	runs   int
	stream bool
}

func (s *stubStreamBrain) Run(context.Context, agent.Input) (*agent.Output, error) {
	s.runs++
	return s.out, nil
}

func (s *stubStreamBrain) RunStream(_ context.Context, _ agent.Input, spl *llm.StreamSplitter) (*agent.Output, error) {
	s.runs++
	s.stream = true
	for _, c := range s.chunks {
		spl.Feed(c)
	}
	spl.Finish()
	return s.out, nil
}

func testStreamFactory(sender *outbound.Sender) func(context.Context, outbound.Target) *llm.StreamSplitter {
	return func(ctx context.Context, target outbound.Target) *llm.StreamSplitter {
		ss := outbound.NewStreamSender(sender, target, outbound.StreamOptions{})
		return llm.NewStreamSplitter(llm.StreamConfig{
			MaxChars: 3, FirstMinChars: 1, Flush: ss.Handler(ctx),
		})
	}
}

// Test_F64_StreamedReplyIsSentIncrementallyWithoutDuplication 是 F-64 的核心验收：
// 增量按句发出，且**不再整段重发**——否则用户会把同一段话看两遍。
func Test_F64_StreamedReplyIsSentIncrementallyWithoutDuplication(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubStreamBrain{
		out:    &agent.Output{Text: "第一句。第二句。", FinishReason: "stop"},
		chunks: []llm.Chunk{{Content: "第一句。"}, {Content: "第二句。"}, {Done: true, FinishReason: "stop"}},
	}
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		NewStreamSplitter: testStreamFactory(sender),
	})
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}
	p.Handle(ctx, testJob(key))

	if !brain.stream {
		t.Fatal("实现了 StreamingAgent 的 Brain 应走流式路径")
	}
	// 两句各触发一次增量发送；整段文本不得再发一遍。
	if got := caller.count(); got != 2 {
		t.Fatalf("发送次数=%d，want 2（两条增量，无整段重发）", got)
	}
	sess, ok := mgr.Get(key)
	if !ok {
		t.Fatal("会话未建立")
	}
	items, err := sess.Hist.Messages(ctx, key.String())
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("历史应只有 用户+助手 两条，实际 %d: %+v", len(items), items)
	}
	if items[1].Content != "第一句。第二句。" {
		t.Fatalf("助手轮内容=%q", items[1].Content)
	}
}

// Test_F64_StreamingDisabledSendsOneWholeReply 说明"没启用流式"时的行为完全不变。
func Test_F64_StreamingDisabledSendsOneWholeReply(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubStreamBrain{out: &agent.Output{Text: "整段回答", FinishReason: "stop"}}
	p := New(Deps{Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second})
	p.Handle(ctx, testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3}))
	if brain.stream {
		t.Fatal("未注入切分器时不应走流式")
	}
	if got := caller.count(); got != 1 {
		t.Fatalf("应整段发一条，实际 %d 条", got)
	}
}

// Test_F64_NonStreamingBrainFallsBackToWholeReply 钉住 ReAct 的边界：
// 只实现 Agent 的 Brain（如 ReactAgent）即使配置了流式也必须退回整段发送。
func Test_F64_NonStreamingBrainFallsBackToWholeReply(t *testing.T) {
	ctx := context.Background()
	lg := testLogger(t)
	mgr := testSessions(t)
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, outbound.New())
	brain := &stubBrain{out: &agent.Output{Text: "整段回答", FinishReason: "stop"}}
	p := New(Deps{
		Brain: brain, Sessions: mgr, Sender: sender, Log: lg, Timeout: 5 * time.Second,
		NewStreamSplitter: testStreamFactory(sender),
	})
	p.Handle(ctx, testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3}))
	if brain.calls != 1 {
		t.Fatalf("Brain 调用次数=%d", brain.calls)
	}
	if got := caller.count(); got != 1 {
		t.Fatalf("不支持流式的 Brain 应整段发一条，实际 %d 条", got)
	}
}
