package main

import (
	"context"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/backpressure"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// eventIngress 是**事件入口**：有界背压队列 + 传输读循环回调（Sink）。
//
// 抽出来的目的不是缩短 serve()，而是让两条此前只写在注释里的行为有名字、可验证：
//
//  1. **绝不在 Sink 里调用平台 API**。传输读完一帧后是同步调用 Sink 的，
//     而 API 的响应也只能由同一个读循环读回来——在那里 Call 必然死锁。
//     引用解析因此被推迟到回复 worker 里（见 internal/reply）。
//  2. **临时路由优先于常规路由**。命中即消费，不再进入常规路由，
//     否则 Await 等的那条消息会被常规路由再处理一遍。
type eventIngress struct {
	d     ingressDeps
	queue *backpressure.Queue[eventJob]
}

// ingressDeps 是事件入口的装配件。
type ingressDeps struct {
	Engine   *router.Engine
	Sessions *session.Manager
	Catalog  *metrics.Catalog
	Log      *observe.Logger
	// ListenCtx 用来给入站事件建立 trace 上下文；它在读循环启动前就已创建，
	// 因此事件能带着一条可贯穿日志与出站请求的 trace。
	ListenCtx context.Context
	// AccessCtl / SuperUsers 用于把"这次发言是谁送的"解析成角色放进 ctx。
	AccessCtl  *accessControls
	SuperUsers map[int64]struct{}
}

// newEventIngress 装配事件入口。
//
// 队列有界：洪峰时按 DropNewest 丢弃并计数，绝不在读循环里阻塞（F-20）。
func newEventIngress(d ingressDeps) *eventIngress {
	ing := &eventIngress{d: d}
	ing.queue = backpressure.New(func(j eventJob) {
		d.Engine.Dispatch(j.ctx, j.event, j.caller)
	}, backpressure.Options{
		OnDrop: func(reason string) {
			d.Catalog.EventsDropped.With(metrics.Labels{"reason": reason}).Inc()
		},
		OnPanic: func(recovered any) {
			d.Log.Component("transport").Error("event handler panic", "panic", fmt.Sprint(recovered))
		},
	})
	return ing
}

// Queue 暴露底层队列：指标闭包要读它的深度。
func (i *eventIngress) Queue() *backpressure.Queue[eventJob] { return i.queue }

// Start 启动队列的 worker。
func (i *eventIngress) Start() { i.queue.Start() }

// Close 在给定预算内排空队列。
func (i *eventIngress) Close(ctx context.Context) error { return i.queue.Close(ctx) }

// Sink 是传输读循环的回调：解析帧 → 临时路由 → 入队。
//
// 这个函数必须**立即返回**：它跑在读循环里，任何阻塞都会让整个平台连接停摆。
func (i *eventIngress) Sink(raw []byte, caller transport.Caller) {
	d := i.d
	ev := event.NewEvent(raw)
	tlog := d.Log.Component("transport")
	if ev.Kind == "" {
		tlog.Debug("ignored frame without post_type", "bytes", len(raw))
		return
	}
	if ev.DecodeWarning != "" {
		// 解析降级必须可见：它是"看起来没反应"的第一手线索。
		tlog.Warn("event decoded with warnings",
			"warning", ev.DecodeWarning, "segments", segmentTypes(ev.Message))
	}
	d.Catalog.EventsReceived.With(metrics.Labels{"kind": string(ev.Kind)}).Inc()
	tlog.Debug("event received",
		"kind", string(ev.Kind), "sub", ev.Sub, "self_id", ev.SelfID,
		"user_id", ev.UserID, "group_id", ev.GroupID, "message_id", ev.MessageID.String(),
		"segments", segmentTypes(ev.Message), "summary", ev.Message.Summary())
	if detail := segmentDetail(ev.Message); detail != "" {
		tlog.Debug("non-text segment fields", "detail", detail)
	}

	// F-15：会话级临时路由优先于常规路由。命中即消费，不再进入常规路由——
	// 否则 Await 等待的那条消息会同时被常规路由处理一遍。
	//nolint:contextcheck // Offer 只在过期清理时做后台收尾，事件循环本身没有请求 ctx
	if d.Sessions.Temp().Offer(d.Sessions.KeyFor(ev.SelfID, ev.GroupID, ev.UserID), ev) {
		tlog.Debug("event consumed by a temporary route",
			"self_id", ev.SelfID, "user_id", ev.UserID, "group_id", ev.GroupID)
		return
	}

	// F-53：把已解析的角色放进 ctx——提示词侧渲染与执行侧拦截都用它。
	ectx := policy.WithRole(eventTraceContext(d.ListenCtx, ev), roleForEvent(ev, d.AccessCtl, d.SuperUsers))
	i.queue.Submit(eventJob{ctx: ectx, event: ev, caller: caller})
}
