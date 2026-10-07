package main

import (
	"strings"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// jobEnqueuer 把一次路由命中转成回复任务并入队。
//
// 这段逻辑此前是 serve() 里的一个内联闭包，承载两条容易写错的行为：
//
//  1. 正文取自 Message.Summary() 而**不是** PlainText()。纯表情 / 纯图片消息的
//     PlainText 是空串，用它做判空会把这类消息静默丢弃——既没回复，日志里也没有痕迹。
//  2. 队列满时**必须回滚在途计数**。先 Add 再入队，入队失败就该 Done；
//     漏掉这一步，优雅关闭会一直等一个永远不会执行的任务，直到预算耗尽。
type jobEnqueuer struct {
	jobs       chan<- reply.Job
	sessions   *session.Manager
	inflight   *sync.WaitGroup
	catalog    *metrics.Catalog
	accessCtl  *accessControls
	superUsers map[int64]struct{}
	log        *observe.Logger
}

// Enqueue 构造任务并投递；队列满时丢弃并回滚在途计数。
//
// shouldReply=false 表示"只记录不回复"：群聊里没被 @ 的环境消息仍然入队，
// 它们是模型理解"刚才在聊什么"的依据。
func (e jobEnqueuer) Enqueue(c *router.Ctx, shouldReply bool) {
	text := strings.TrimSpace(c.Event.Message.Summary())
	if text == "" {
		return
	}
	e.inflight.Add(1)
	select {
	case e.jobs <- reply.Job{
		Key:         e.sessions.KeyFor(c.Event.SelfID, c.Event.GroupID, c.Event.UserID),
		GroupID:     c.Event.GroupID,
		UserID:      c.Event.UserID,
		Text:        text,
		TraceID:     observe.TraceID(c),
		Role:        agentRoleFor(c.Event, e.accessCtl, e.superUsers),
		PolicyRole:  roleForEvent(c.Event, e.accessCtl, e.superUsers),
		ShouldReply: shouldReply,
		SpeakerID:   groupScopedUserID(c.Event.UserID, c.Event.GroupID),
		SpeakerName: speakerDisplayName(c.Event.Sender, c.Event.GroupID),
		// 记忆按群共享，因此还要带上"这条记忆关于谁"——群聊里就是发言人 QQ 号。
		// 私聊的 SpeakerID 为 0（整条会话就是这一个人），归属留空即可。
		SubjectID: groupScopedUserID(c.Event.UserID, c.Event.GroupID),
		Message:   c.Event.Message,
		Caller:    c.Caller(),
	}:
	default:
		e.inflight.Done()
		e.catalog.EventsDropped.With(metrics.Labels{"reason": "queue_full"}).Inc()
		e.log.Component("lifecycle").Warn("reply queue is full; dropping message", "user_id", c.Event.UserID)
	}
}
