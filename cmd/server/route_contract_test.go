package main

import (
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/router"
)

// buildRouteTable 按给定顺序构造路由表，用于驱动契约检查。
// specs 形如 "名称:early|late|normal"；normal 表示保留默认优先级，
// 这样"插入顺序"就是最终顺序（priority 相同时 Snapshot 用稳定排序）。
func buildRouteTable(t *testing.T, specs ...string) *router.Router {
	t.Helper()
	r := router.NewRouter()
	for _, spec := range specs {
		name, prio, _ := strings.Cut(spec, ":")
		rt := r.OnMessage(router.Always()).Named(name).Handle(func(*router.Ctx) {})
		switch prio {
		case "early":
			rt.Priority(router.PriorityEarly)
		case "late":
			rt.Priority(router.PriorityLate)
		}
	}
	return r
}

// Test_CheckRouteTableAcceptsTheRealOrder 正常装配必须通过。
func Test_CheckRouteTableAcceptsTheRealOrder(t *testing.T) {
	t.Parallel()
	r := buildRouteTable(t, "switch:early", "reply:early", "record:late")
	if err := checkRouteTable(r, inboundRouteContract); err != nil {
		t.Fatalf("正常顺序不该报错: %v", err)
	}
}

// Test_CheckRouteTableAcceptsMissingConditionalRoute /switch 只在开关启用时注册。
func Test_CheckRouteTableAcceptsMissingConditionalRoute(t *testing.T) {
	t.Parallel()
	r := buildRouteTable(t, "reply:early", "record:late")
	if err := checkRouteTable(r, inboundRouteContract); err != nil {
		t.Fatalf("条件路由缺失不该报错: %v", err)
	}
}

// 注意 router 的排序语义：Snapshot 按 **priority 稳定排序**，同优先级才看注册顺序。
// 所以"兜底路由不是最后一条"最现实的成因是**在同优先级下注册得更早**，
// 或者**在它之后又追加了一条路由**——后者正是最常见的改错方式。

// Test_CheckRouteTableRejectsCatchAllNotLast 钉住最危险的一种改法。
//
// 兜底路由排到前面会吃掉后面所有路由：机器人**完全不回复**，
// 而进程健康、探针正常、日志无异常——编译和单测都不会发现。
func Test_CheckRouteTableRejectsCatchAllNotLast(t *testing.T) {
	t.Parallel()
	// 同为 early 优先级，record 注册在前 → 稳定排序后 record 排第一。
	r := buildRouteTable(t, "record:early", "reply:early")
	err := checkRouteTable(r, inboundRouteContract)
	if err == nil {
		t.Fatal("兜底路由不在最后必须被拒绝")
	}
	if !strings.Contains(err.Error(), "必须排在最后") {
		t.Fatalf("错误信息应点明顺序问题，实际: %v", err)
	}
}

// Test_CheckRouteTableRejectsRouteAddedAfterCatchAll 钉住最常见的改错方式。
//
// 在兜底路由**之后**追加一条路由，那条路由永远不会被执行——
// 而看代码时它就在最后，很像"注册好了"。
func Test_CheckRouteTableRejectsRouteAddedAfterCatchAll(t *testing.T) {
	t.Parallel()
	// 全部使用默认优先级：此时 Snapshot 的稳定排序直接反映注册顺序。
	r := buildRouteTable(t, "reply:normal", "record:normal", "later-added:normal")
	err := checkRouteTable(r, inboundRouteContract)
	if err == nil {
		t.Fatal("兜底路由之后还有路由必须被拒绝")
	}
	if !strings.Contains(err.Error(), "必须排在最后") {
		t.Fatalf("错误信息应点明顺序问题，实际: %v", err)
	}
}

// Test_CheckRouteTablePriorityDominatesRegistrationOrder 记录一条容易误解的语义：
// 优先级不同时，注册顺序不影响最终顺序（稳定排序按 priority）。
//
// 这条断言不是在"允许"乱序，而是说明上面那条检查能覆盖到哪些形状。
func Test_CheckRouteTablePriorityDominatesRegistrationOrder(t *testing.T) {
	t.Parallel()
	r := buildRouteTable(t, "record:late", "reply:early")
	if err := checkRouteTable(r, inboundRouteContract); err != nil {
		t.Fatalf("优先级已保证兜底在后，不该报错: %v", err)
	}
}

// Test_CheckRouteTableRejectsEmptyTable 空表等于没有任何路由，必须报错。
func Test_CheckRouteTableRejectsEmptyTable(t *testing.T) {
	t.Parallel()
	if err := checkRouteTable(router.NewRouter(), inboundRouteContract); err == nil {
		t.Fatal("空路由表必须被拒绝")
	}
}

// Test_CheckRouteTableRejectsHandlerlessRoute 没有 handler 的路由匹配上了也什么都不做。
func Test_CheckRouteTableRejectsHandlerlessRoute(t *testing.T) {
	t.Parallel()
	r := router.NewRouter()
	r.OnMessage(router.Always()).Named("nameonly")
	if err := checkRouteTable(r, routeContract{Early: nil, CatchAll: "nameonly"}); err == nil {
		t.Fatal("没有 handler 的路由必须被拒绝")
	}
}

// Test_CheckRouteTableRejectsContractDrift 契约点名的兜底路由一个都没注册时，
// 这条检查本身已经失效，必须说出来而不是静默通过。
func Test_CheckRouteTableRejectsContractDrift(t *testing.T) {
	t.Parallel()
	r := buildRouteTable(t, "reply:early")
	err := checkRouteTable(r, inboundRouteContract)
	if err == nil {
		t.Fatal("兜底路由没注册必须被拒绝")
	}
	if !strings.Contains(err.Error(), "形同虚设") {
		t.Fatalf("错误信息应点明契约脱节，实际: %v", err)
	}
}
