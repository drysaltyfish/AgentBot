package main

import (
	"fmt"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/router"
)

// routeContract 描述装配必须满足的路由形状。
//
// 为什么要有它：路由的**顺序就是行为**。`record` 是 catch-all（`router.Always()`），
// 它一旦排到 `reply` 前面，就会把每条消息都吃掉——机器人表现为**完全不回复**，
// 而进程健康、探针正常、日志无异常。这类改动不会编译失败，也不会让任何单测变红。
type routeContract struct {
	// Early 是必须排在 catch-all 之前的路由名（按此顺序）；条件注册的路由缺失不算错。
	Early []string
	// CatchAll 是兜底路由名：必须存在，且必须是最后一条。
	CatchAll string
}

// inboundRouteContract 是入站路由表的契约，与 serve.go 里注册的那张表一一对应。
var inboundRouteContract = routeContract{
	Early:    []string{"switch", "reply"},
	CatchAll: "record",
}

// checkRouteTable 校验实际注册的路由表符合契约。
//
// 三条检查：
//  1. 兜底路由必须存在——不存在说明装配的名字与契约脱节了（这条检查形同虚设）；
//  2. 兜底路由必须是**最后一条**——它不是最后一条就会遮住后面的路由；
//  3. 契约里点名的 early 路由（存在的那些）必须排在兜底之前，且相对顺序正确。
//
// 另外每条路由至少要有一个 handler：没有 handler 的路由匹配上了也什么都不做。
func checkRouteTable(routes *router.Router, c routeContract) error {
	if routes == nil {
		// 没有路由表时无从校验；这与"空表"不同，交给调用方决定要不要注册。
		return nil
	}
	infos := routes.Routes()
	if len(infos) == 0 {
		return fmt.Errorf("路由表为空：契约要求至少有一条兜底路由 %q", c.CatchAll)
	}

	pos := make(map[string]int, len(infos))
	for i, info := range infos {
		// 同名路由只记第一次出现的位置：router 会对重名告警，但那不是本检查的职责。
		if _, seen := pos[info.Name]; !seen {
			pos[info.Name] = i
		}
		if info.Handlers == 0 {
			return fmt.Errorf("路由 %q 没有 handler：它匹配上了也不会做任何事", info.Name)
		}
	}

	catchAll, ok := pos[c.CatchAll]
	if !ok {
		return fmt.Errorf("契约里的兜底路由 %q 没有注册：装配的名字与契约脱节了，这条检查已经形同虚设", c.CatchAll)
	}
	if catchAll != len(infos)-1 {
		return fmt.Errorf("兜底路由 %q 必须排在最后，实际是第 %d/%d 条（它后面的路由永远不会被执行）",
			c.CatchAll, catchAll+1, len(infos))
	}

	last := -1
	for _, name := range c.Early {
		i, ok := pos[name]
		if !ok {
			continue // 条件注册的路由（例如 /switch）不启用时不存在，不算违规。
		}
		if i >= catchAll {
			return fmt.Errorf("路由 %q 必须排在兜底路由 %q 之前，实际是第 %d/%d 条",
				name, c.CatchAll, i+1, len(infos))
		}
		if i < last {
			return fmt.Errorf("路由 %q 与契约顺序不符：期望顺序 [%s]",
				name, strings.Join(c.Early, " > "))
		}
		last = i
	}
	return nil
}
