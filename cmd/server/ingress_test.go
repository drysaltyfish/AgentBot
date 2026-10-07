package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// ingressFrame 造一条群消息帧（平台角色可控）。
func ingressFrame(userID, groupID int64, role string) []byte {
	return []byte(fmt.Sprintf(
		`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":%d,"group_id":%d,"message_id":1,"sender":{"user_id":%d,"role":%q},"message":[{"type":"text","data":{"text":"hi"}}]}`,
		userID, groupID, userID, role))
}

// newTestIngress 造一个事件入口：一条兜底路由，命中时把角色送到 channel。
func newTestIngress(t *testing.T) (*eventIngress, *session.Manager, <-chan string) {
	t.Helper()
	routes := router.NewRouter()
	engine := router.NewEngine(routes)
	got := make(chan string, 4)
	routes.OnMessage().Handle(func(c *router.Ctx) { got <- policy.RoleFrom(c) })

	sessions := session.New()
	cfg := config.Default()
	cfg.Transport.SelfID = ptr(int64(10001))
	controls := buildAccessControls(cfg, nil)

	ing := newEventIngress(ingressDeps{
		Engine:     engine,
		Sessions:   sessions,
		Catalog:    metrics.NewCatalog(metrics.CatalogOptions{}),
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
	return ing, sessions, got
}

// Test_EventIngressPutsRoleIntoTheJobContext 钉住 sink 的最后一步。
//
// F-53 的执行侧硬拦截与提示词侧渲染都从 ctx 里读角色；
// 漏掉这一行会退化成"按 everyone 判定"，表现是工具莫名被判越权。
func Test_EventIngressPutsRoleIntoTheJobContext(t *testing.T) {
	t.Parallel()
	ing, _, got := newTestIngress(t)

	ing.Sink(ingressFrame(42, 900, "owner"), nil)

	select {
	case role := <-got:
		if role != policy.RoleOwner {
			t.Fatalf("ctx 里的角色: actual=%q expected=%q", role, policy.RoleOwner)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("事件没有被派发到路由")
	}
	// 队列最终应排空（worker 消费掉）。
	waitForDepth(t, ing.Queue(), 0)
}

// Test_EventIngressTempRouteConsumesBeforeRouting 钉住临时路由的优先级。
//
// 临时路由（F-15 的 Await / Stream）必须先于常规路由消费事件；
// 否则 Await 等的那条消息会被常规路由再处理一遍——
// 表现是"等待中的那条消息同时触发了一次正常回复"。
func Test_EventIngressTempRouteConsumesBeforeRouting(t *testing.T) {
	t.Parallel()
	ing, sessions, got := newTestIngress(t)

	key := sessions.KeyFor(10001, 900, 42)
	delivered := make(chan struct{}, 1)
	remove := sessions.Temp().Register(session.TempRoute{
		Key:     key,
		Once:    true,
		Deliver: func(*event.Event) { delivered <- struct{}{} },
	})
	defer remove()

	ing.Sink(ingressFrame(42, 900, "member"), nil)

	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("临时路由没有收到事件")
	}
	// 被临时路由消费的事件不得再进入常规路由，也不该入队。
	select {
	case role := <-got:
		t.Fatalf("被临时路由消费的事件不该再进常规路由（角色=%q）", role)
	case <-time.After(300 * time.Millisecond):
	}
	if depth := ing.Queue().Depth(); depth != 0 {
		t.Fatalf("被临时路由消费的事件不该入队，队列深度=%d", depth)
	}
}

// Test_EventIngressDropsFramesWithoutPostType 覆盖读循环里的"坏帧"路径。
//
// sink 跑在读循环上，任何 panic 或阻塞都会让整条平台连接停摆，
// 因此解析不出 post_type 的帧必须被安静丢弃。
//
// 注意边界：只有**没有 post_type** 才算坏帧。带 post_type 但字段不全的帧
// 仍然会被转发——那是路由层的事，sink 不替它做判断。
func Test_EventIngressDropsFramesWithoutPostType(t *testing.T) {
	t.Parallel()
	ing, _, got := newTestIngress(t)

	for _, raw := range []string{
		`{}`,       // 没有 post_type
		`not json`, // 完全不是 JSON
		``,         // 空帧
	} {
		ing.Sink([]byte(raw), nil)
	}

	select {
	case role := <-got:
		t.Fatalf("没有 post_type 的帧不该被派发到路由（角色=%q）", role)
	case <-time.After(300 * time.Millisecond):
	}
	if depth := ing.Queue().Depth(); depth != 0 {
		t.Fatalf("坏帧不该入队，队列深度=%d", depth)
	}
}

// Test_EventIngressForwardsTypedFramesEvenWhenSparse 钉住"边界在哪"。
//
// 带 post_type 的帧即使字段不全也照常转发：sink 只负责"这帧能不能解析"，
// "这帧有没有意义"是路由层与处理层的判断。把后者塞进读循环只会让它更脆。
func Test_EventIngressForwardsTypedFramesEvenWhenSparse(t *testing.T) {
	t.Parallel()
	ing, _, got := newTestIngress(t)

	ing.Sink([]byte(`{"post_type":"message"}`), nil)

	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("带 post_type 的帧应当被转发（是否处理由路由决定）")
	}
}

// waitForDepth 等到队列深度达到期望值（worker 是异步消费的）。
func waitForDepth(t *testing.T, q interface{ Depth() int }, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q.Depth() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("队列深度未在期限内达到 %d（当前 %d）", want, q.Depth())
}
