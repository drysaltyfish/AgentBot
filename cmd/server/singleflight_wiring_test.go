package main

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// privateMsg 是单飞测试用的一条私聊消息。
const privateMsg = `{"post_type":"message","message_type":"private","sub_type":"friend",` +
	`"self_id":10001,"user_id":20002,"message_id":3,"sender":{"user_id":20002,"role":"member"},` +
	`"message":[{"type":"text","data":{"text":"hello there"}}]}`

// Test_UseSingleflightRegistersBothHalves 钉住单飞"两半"必须一起注册。
//
// 判定的方式不是数钩子个数，而是**行为**：同一个 key 连投两条消息，
// 两条都必须被处理——第二条能被处理就说明第一条的占位被 post 释放了。
func Test_UseSingleflightRegistersBothHalves(t *testing.T) {
	t.Parallel()
	r := router.NewRouter()
	engine := router.NewEngine(r)
	sf := router.NewSingleflight(func(c *router.Ctx) int64 { return c.Event.UserID })
	useSingleflight(engine, sf)

	handled := 0
	r.OnMessage(router.Always()).Handle(func(*router.Ctx) { handled++ })

	for i := 0; i < 2; i++ {
		engine.Dispatch(context.Background(), event.NewEvent([]byte(privateMsg)), nil)
	}
	if handled != 2 {
		t.Fatalf("同一 key 的第二次消息也应被处理（说明 post 释放了占位），实际处理 %d 次", handled)
	}
	if n := sf.Inflight(); n != 0 {
		t.Fatalf("全部处理完后不该有残留占位，实际 %d", n)
	}
}

// Test_SingleflightWithoutReleaseMutesTheSession 把"漏掉 Release"的后果写成断言。
//
// 这条断言的是**危险行为本身**：只挂 mid 的 Rule 时，第一条消息占位后没有任何地方释放，
// 于是同一个 key 之后每条消息都被拒绝——机器人每个会话只答一次，然后彻底沉默。
//
// 它存在的意义：一旦有人把 useSingleflight 拆回两行、只留下 Rule，
// 这条会提醒他这不是"单飞降级成直通"，而是永久拒绝。
func Test_SingleflightWithoutReleaseMutesTheSession(t *testing.T) {
	t.Parallel()
	r := router.NewRouter()
	engine := router.NewEngine(r)
	sf := router.NewSingleflight(func(c *router.Ctx) int64 { return c.Event.UserID })
	engine.UseMid(sf.Rule()) // 故意不注册 Release

	handled := 0
	r.OnMessage(router.Always()).Handle(func(*router.Ctx) { handled++ })

	for i := 0; i < 3; i++ {
		engine.Dispatch(context.Background(), event.NewEvent([]byte(privateMsg)), nil)
	}
	if handled != 1 {
		t.Fatalf("漏掉 Release 时只有第一条会被处理，实际 %d 次", handled)
	}
	if n := sf.Inflight(); n != 1 {
		t.Fatalf("占位应永久残留（这正是危害所在），实际 %d", n)
	}
}
