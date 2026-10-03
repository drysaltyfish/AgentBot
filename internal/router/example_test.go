package router_test

import (
	"context"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// Example_replyPolicyRoutes 演示 C3 的目标注册形态（可在 cmd/server/main.go 直接照抄）：
// 一条谓词路由承载回复策略，一条 Always 路由只做记录（全量入队）。
func Example_replyPolicyRoutes() {
	routes := router.NewRouter()
	engine := router.NewEngine(routes)

	// 回复策略：只有私聊才回复（真实代码里换成 replyRule(cfg)）。
	routes.OnMessage(router.OnlyPrivate()).
		Named("reply").
		Priority(router.PriorityEarly).
		Handle(func(c *router.Ctx) { fmt.Println("reply", c.Event.UserID) })

	// 只记录：所有消息都入队，不受回复策略影响。
	routes.OnMessage(router.Always()).
		Named("record").
		Priority(router.PriorityLate).
		Handle(func(c *router.Ctx) { fmt.Println("record", c.Event.UserID) })

	const privateMsg = `{"post_type":"message","message_type":"private","sub_type":"friend","self_id":10001,"user_id":20002,"message_id":3,"message":[{"type":"text","data":{"text":"hi"}}]}`
	n := engine.Dispatch(context.Background(), event.NewEvent([]byte(privateMsg)), nil)
	fmt.Println("matched", n)

	// Output:
	// reply 20002
	// record 20002
	// matched 2
}
