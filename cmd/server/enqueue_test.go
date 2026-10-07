package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// newTestEnqueuer 造一个入队器：可控容量的队列 + 独立的在途计数。
func newTestEnqueuer(t *testing.T, capacity int) (jobEnqueuer, chan reply.Job, *sync.WaitGroup) {
	t.Helper()
	cfg := config.Default()
	cfg.Transport.SelfID = ptr(int64(10001))
	controls := buildAccessControls(cfg, nil)
	jobs := make(chan reply.Job, capacity)
	wg := &sync.WaitGroup{}
	return jobEnqueuer{
		jobs:       jobs,
		sessions:   session.New(),
		inflight:   wg,
		catalog:    metrics.NewCatalog(metrics.CatalogOptions{}),
		accessCtl:  controls,
		superUsers: superUsersFrom(controls.Roles),
		log:        testLogger(t),
	}, jobs, wg
}

// emojiOnlyFrame 是一条**只有表情**的群消息：PlainText 为空，Summary 非空。
func emojiOnlyFrame() []byte {
	return []byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":42,"group_id":900,"message_id":1,"sender":{"user_id":42,"role":"member"},"message":[{"type":"face","data":{"id":"4"}}]}`)
}

// Test_JobEnqueuerAcceptsEmojiOnlyMessage 钉住"纯表情/纯图片消息也要能被回复"。
//
// 正文若取自 Message.PlainText()，这类消息会得到空串并被静默丢弃——
// 用户看到的是"机器人对我的表情毫无反应"，而日志里什么线索都没有。
// 所以必须用 Summary()。
func Test_JobEnqueuerAcceptsEmojiOnlyMessage(t *testing.T) {
	t.Parallel()
	enq, jobs, _ := newTestEnqueuer(t, 4)

	ev := event.NewEvent(emojiOnlyFrame())
	if ev.Message.PlainText() != "" {
		t.Fatalf("前置条件不成立：纯表情消息的 PlainText 应为空，实际 %q", ev.Message.PlainText())
	}
	if ev.Message.Summary() == "" {
		t.Fatal("前置条件不成立：Summary 对纯表情消息应给出占位文本")
	}

	enq.Enqueue(router.NewCtx(context.Background(), ev, nil), true)

	select {
	case j := <-jobs:
		if j.Text == "" {
			t.Fatal("纯表情消息必须仍能入队（正文取自 Summary，不是 PlainText）")
		}
		if !j.ShouldReply {
			t.Fatal("ShouldReply 应随参数透传")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("纯表情消息被静默丢弃了")
	}
}

// Test_JobEnqueuerRollsBackInflightWhenQueueIsFull 钉住队满时的在途回滚。
//
// 入队前 Add、入队失败 Done，两者必须成对：漏掉 Done 会让优雅关闭
// 一直等一个永远不会执行的任务，直到预算耗尽（表现为"关闭很慢"，
// 而不是任何显式错误）。
func Test_JobEnqueuerRollsBackInflightWhenQueueIsFull(t *testing.T) {
	t.Parallel()
	enq, jobs, wg := newTestEnqueuer(t, 1) // 只放得下一条

	ev := event.NewEvent(emojiOnlyFrame())
	ctx := router.NewCtx(context.Background(), ev, nil)

	enq.Enqueue(ctx, true) // 占满
	enq.Enqueue(ctx, true) // 应被丢弃并回滚

	if depth := len(jobs); depth != 1 {
		t.Fatalf("队满时不应再入队: depth=%d", depth)
	}

	// 模拟 worker 消费掉那一条：在途计数应当归零。
	<-jobs
	wg.Done()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("在途计数没有回到 0：队满时漏了 Done，优雅关闭会一直等")
	}
}

// Test_JobEnqueuerCarriesPolicyRoleAndShouldReply 覆盖字段映射里最容易出错的几个。
//
// PolicyRole 与 Role 是两套角色名空间（policy 用于 API 硬拦截，agent 用于审批闸门），
// 漏带 PolicyRole 的表现是工具按 everyone 判越权——一个只在群聊里出现、
// 且报错藏在工具结果里的静默故障。
func Test_JobEnqueuerCarriesPolicyRoleAndShouldReply(t *testing.T) {
	t.Parallel()
	enq, jobs, _ := newTestEnqueuer(t, 4)

	ev := event.NewEvent([]byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":42,"group_id":900,"message_id":1,"sender":{"user_id":42,"role":"owner"},"message":[{"type":"text","data":{"text":"hi"}}]}`))
	enq.Enqueue(router.NewCtx(context.Background(), ev, nil), false)

	select {
	case j := <-jobs:
		if j.PolicyRole == "" {
			t.Fatal("PolicyRole 必须被填充（漏带会让工具按 everyone 判越权）")
		}
		if j.ShouldReply {
			t.Fatal("shouldReply=false 表示只记录不回复（群里的环境消息）")
		}
		if j.Text != "hi" {
			t.Fatalf("Text: actual=%q expected=%q", j.Text, "hi")
		}
		if j.SpeakerID != 42 {
			t.Fatalf("群聊里 SpeakerID 应为发言人 QQ: actual=%d", j.SpeakerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("普通文本消息没有被入队")
	}
}
