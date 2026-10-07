package reply

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/semcache"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
	"github.com/drysaltyfish/agentbot/internal/vector"
)

// filterMarker 是测试用的可辨认痕迹，任何绕过出口过滤链的路径都不会带上它。
const filterMarker = "⟦filtered⟧"

// markedChain 构造一条带标记的出口过滤链。
//
// 标记挂在 **FilterNormalize** 上，而不是自己起一个新名字：`Chain.Apply` 只按固定的
// `Order` 遍历已知位置，用新名字注册的 filter **根本不会被执行**——
// 而测试照样通过，留下"我验过过滤了"的假象（本仓库里就有这样一个例子，
// 见 Test_F63_CacheHitGoesThroughTheSendChain 的注释）。
// Normalize 是顺序里的最后一个，所以标记不会被后续 filter 抹掉。
func markedChain() *outbound.Chain {
	return outbound.New(outbound.WithFilter(outbound.FilterNormalize, func(s string) string {
		if s == "" {
			return s
		}
		return s + filterMarker
	}))
}

// Test_F55_ReplyContentReachesThePlatformFiltered 钉住 F-55 的验收：
// **所有发送路径都经过出口过滤链**。
//
// 判据刻意是"到达平台的内容**已经被处理过**"，而不是"过滤链被调用了"：
// 后者在路径绕开 Sender 直接调 Caller 时依然可能为真（链被调用了，
// 只是没作用在这条消息上）。做法与规格给的一致——用 RecordingCaller 抓住
// 真正发出去的请求，再检查内容带着链的痕迹。
func Test_F55_ReplyContentReachesThePlatformFiltered(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, markedChain())

	brain := &stubBrain{out: &agent.Output{Text: "收到啦", FinishReason: "stop"}}
	p := New(Deps{
		Brain: brain, Sessions: testSessions(t), Sender: sender,
		Log: testLogger(t), Timeout: 5 * time.Second,
	})

	p.Handle(context.Background(), testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3}))

	reqs := recordedRequests(caller)
	if len(reqs) == 0 {
		t.Fatal("没有任何请求到达平台：这条断言的前提不成立")
	}
	assertEveryMessageIsFiltered(t, reqs, 0)
}

// Test_F55_CachedReplyAlsoGoesThroughTheChain 语义缓存命中同样是一条发送路径。
//
// 单独断言是因为"缓存命中绕开出口过滤"是一类真实的历史故障形态：
// 答案从缓存里取出后如果直接发出去，用户内容就绕过了出口处理。
func Test_F55_CachedReplyAlsoGoesThroughTheChain(t *testing.T) {
	t.Parallel()
	caller := &recordingCaller{}
	sender := outbound.NewSender(caller, markedChain())

	cache, err := semcache.New(semcache.Options{
		Vectorize: func(q string) vector.Binary { return vector.TextBinary(q, vector.TextDim) },
	})
	if err != nil {
		t.Fatalf("semcache.New: %v", err)
	}
	brain := &stubBrain{out: &agent.Output{Text: "缓存答案", FinishReason: "stop"}}
	p := New(Deps{
		Brain: brain, Sessions: testSessions(t), Sender: sender, Log: testLogger(t),
		Timeout: 5 * time.Second, Semcache: cache,
		SemcacheFingerprint: func(context.Context, session.Key) string { return "p" },
	})

	job := testJob(session.Key{SelfID: 1, GroupID: 2, UserID: 3})
	job.Text = "同一个问题"
	p.Handle(context.Background(), job) // 未命中：走模型
	before := len(recordedRequests(caller))
	p.Handle(context.Background(), job) // 命中：走缓存

	reqs := recordedRequests(caller)
	if len(reqs) <= before {
		t.Fatal("缓存命中没有发出任何消息（本测试前提不成立）")
	}
	assertEveryMessageIsFiltered(t, reqs[before:], before)
}

// assertEveryMessageIsFiltered 断言这批平台请求里的文字内容都带着过滤链的标记。
func assertEveryMessageIsFiltered(t *testing.T, reqs []transport.Request, offset int) {
	t.Helper()
	for i, req := range reqs {
		raw, err := json.Marshal(req.Params["message"])
		if err != nil {
			t.Fatalf("序列化第 %d 条消息段: %v", offset+i, err)
		}
		if !strings.Contains(string(raw), filterMarker) {
			t.Fatalf("第 %d 条发往平台的消息没有经过出口过滤链（缺少标记）：%s", offset+i, raw)
		}
	}
}

// recordedRequests 复制已记录的平台请求。
func recordedRequests(c *recordingCaller) []transport.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]transport.Request(nil), c.reqs...)
}
