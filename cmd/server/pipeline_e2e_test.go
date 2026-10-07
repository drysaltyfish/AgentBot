package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// privateFrame 造一条私聊文本帧。
func privateFrame(userID int64, text string) []byte {
	return []byte(fmt.Sprintf(
		`{"post_type":"message","message_type":"private","sub_type":"friend","self_id":10001,"user_id":%d,"message_id":2,"sender":{"user_id":%d,"role":"member"},"message":[{"type":"text","data":{"text":%q}}]}`,
		userID, userID, text))
}

// newReplyPipeline 按 serve() 的方式把"入站帧 → 路由 → enqueue → jobs"接起来。
//
// 用**真实的** replyRule 与真实的两条路由（early reply / late record），
// 而不是测试专用的假路由：这样断言的就是线上那条链路。
func newReplyPipeline(t *testing.T) (*eventIngress, <-chan reply.Job) {
	t.Helper()
	routes := router.NewRouter()
	engine := router.NewEngine(routes)
	jobs := make(chan reply.Job, 8)
	inflight := &sync.WaitGroup{}
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	sessions := session.New()

	cfg := config.Default()
	cfg.Transport.SelfID = ptr(int64(10001))
	controls := buildAccessControls(cfg, nil)

	enq := jobEnqueuer{
		jobs:       jobs,
		sessions:   sessions,
		inflight:   inflight,
		catalog:    cat,
		accessCtl:  controls,
		superUsers: superUsersFrom(controls.Roles),
		log:        testLogger(t),
	}

	// 与 serve.go 同形：early 的 reply 路由 + late 的 record 兜底。
	routes.OnMessage(replyRule(cfg)).Named("reply").Priority(router.PriorityEarly).
		Block(true).Handle(func(c *router.Ctx) { enq.Enqueue(c, true) })
	routes.OnMessage(router.Always()).Named("record").Priority(router.PriorityLate).
		Handle(func(c *router.Ctx) { enq.Enqueue(c, false) })

	// 顺手确认测试搭出来的表和线上契约同形，避免测试与生产各说各话。
	if err := checkRouteTable(routes, inboundRouteContract); err != nil {
		t.Fatalf("测试装配的路由表不符合线上契约: %v", err)
	}

	ing := newEventIngress(ingressDeps{
		Engine:     engine,
		Sessions:   sessions,
		Catalog:    cat,
		Log:        testLogger(t),
		ListenCtx:  context.Background(),
		AccessCtl:  controls,
		SuperUsers: superUsersFrom(controls.Roles),
	})
	ing.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = ing.Close(ctx)
	})
	return ing, jobs
}

// receiveJob 等一条回复任务。
func receiveJob(t *testing.T, jobs <-chan reply.Job) reply.Job {
	t.Helper()
	select {
	case j := <-jobs:
		return j
	case <-time.After(3 * time.Second):
		t.Fatal("平台帧没有变成回复任务：这条链路断了会表现为机器人收得到消息却从不回复")
		return reply.Job{}
	}
}

// expectNoJob 断言在给定时间内没有任务产生。
func expectNoJob(t *testing.T, jobs <-chan reply.Job, why string) {
	t.Helper()
	select {
	case j := <-jobs:
		t.Fatalf("%s，却产生了任务（should_reply=%v text=%q）", why, j.ShouldReply, j.Text)
	case <-time.After(300 * time.Millisecond):
	}
}

// Test_PrivateFrameBecomesAReplyJob 钉住整条链路：平台帧 → 路由 → 回复任务。
//
// 这是"读循环 → 路由 → 入队"的端到端断言。此前 ingress 的测试只走到路由处理器，
// enqueue 的测试只直接测入队本身，"一条平台消息最终真的变成了一条回复任务"
// 这个跨组件事实没有地方守着。
func Test_PrivateFrameBecomesAReplyJob(t *testing.T) {
	t.Parallel()
	ing, jobs := newReplyPipeline(t)

	ing.Sink(privateFrame(42, "你好"), nil)

	job := receiveJob(t, jobs)
	if !job.ShouldReply {
		t.Fatal("私聊文本按默认配置必须回复（should_reply 应为 true）")
	}
	if job.Text != "你好" {
		t.Fatalf("任务正文: actual=%q expected=%q", job.Text, "你好")
	}
	if job.UserID != 42 {
		t.Fatalf("任务用户: actual=%d expected=42", job.UserID)
	}
	if job.Key.String() == "" {
		t.Fatal("任务必须带会话键，否则每个会话的记忆与历史会串在一起")
	}

	// reply 路由带 Block(true)：命中后不再落到 record 兜底。
	// 漏掉它，同一条消息会产生**两条**任务（一条回复 + 一条记录），
	// 上游会看到重复的上下文与重复的处理。
	expectNoJob(t, jobs, "reply 路由命中后（Block）不应再产生第二条任务")
}

// Test_GroupFrameWithoutMentionIsRecordedButNotReplied 钉住两条路由的分工。
//
// 群聊里没被 @ 的消息走 late 的 record 兜底：**仍然入队**（它是模型理解上下文的依据），
// 但 should_reply=false。若 reply 规则误判成"该回复"，机器人会在群里刷屏。
func Test_GroupFrameWithoutMentionIsRecordedButNotReplied(t *testing.T) {
	t.Parallel()
	ing, jobs := newReplyPipeline(t)

	ing.Sink(ingressFrame(42, 900, "member"), nil)

	job := receiveJob(t, jobs)
	if job.ShouldReply {
		t.Fatal("群里没被 @ 的消息不该回复（默认 group=on_mention）")
	}
	if job.Text != "hi" {
		t.Fatalf("环境消息也要带着正文入队，实际 %q", job.Text)
	}
}

// Test_OwnMessageIsRecordedButNotReplied 机器人自己的消息**记录但不回复**。
//
// 注意它并不是"什么都不做"：reply 规则对自己的消息返回 false，
// 于是事件落到 late 的 record 兜底路由——仍会入队（自己的话也是对话上下文的一部分），
// 只是 should_reply=false。真正要避免的是"回复自己"进而自问自答成环。
func Test_OwnMessageIsRecordedButNotReplied(t *testing.T) {
	t.Parallel()
	ing, jobs := newReplyPipeline(t)

	ing.Sink(privateFrame(10001, "我是机器人自己"), nil) // user_id == self_id

	job := receiveJob(t, jobs)
	if job.ShouldReply {
		t.Fatalf("绝不能回复机器人自己（会自问自答成环），实际 should_reply=true")
	}
	if job.Text != "我是机器人自己" {
		t.Fatalf("自己的消息仍应作为上下文入队，实际正文 %q", job.Text)
	}
}

// Test_FaceOnlyMessageIsStillAReplyJob 钉住"判空用 Summary 而不是 PlainText"这个决定。
//
// 纯表情消息的 PlainText 是空串，用它判空会把这类消息**静默丢弃**：
// 用户发个表情机器人毫无反应，日志里也没有痕迹。改用 Summary 之后，
// 非文本段会被渲染成占位（这里是 `[表情:撇嘴]`），于是它照常变成一条回复任务。
//
// 这条断言的是那个占位语法本身——它决定"带表情的消息会不会被当成空消息丢掉"。
func Test_FaceOnlyMessageIsStillAReplyJob(t *testing.T) {
	t.Parallel()
	ing, jobs := newReplyPipeline(t)

	ing.Sink([]byte(`{"post_type":"message","message_type":"private","sub_type":"friend",`+
		`"self_id":10001,"user_id":42,"message_id":3,"sender":{"user_id":42,"role":"member"},`+
		`"message":[{"type":"face","data":{"id":"1"}}]}`), nil)

	job := receiveJob(t, jobs)
	if job.Text == "" {
		t.Fatal("纯表情消息不该被判成空消息丢弃")
	}
	if job.Text != "[表情:撇嘴]" {
		t.Fatalf("非文本段的占位语法变了，会改变判空结果: actual=%q", job.Text)
	}
	if !job.ShouldReply {
		t.Fatal("私聊里的纯表情消息仍应触发回复")
	}
}

// Test_WhitespaceOnlyFrameIsDroppedBeforeEnqueue 没有任何正文的帧不进回复队列。
//
// 与上面那条的边界相配：Summary 渲染出占位就不算空，渲染后仍是空白才算空。
func Test_WhitespaceOnlyFrameIsDroppedBeforeEnqueue(t *testing.T) {
	t.Parallel()
	ing, jobs := newReplyPipeline(t)

	ing.Sink(privateFrame(42, "   "), nil)

	expectNoJob(t, jobs, "只有空白字符的消息")
}
