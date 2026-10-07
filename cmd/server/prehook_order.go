package main

import (
	"fmt"
	"strings"
)

// 引擎级 pre 钩子的名字。
//
// 用常量而不是散落的字面量：契约与装配必须引用同一个名字，
// 否则任何一边改名都会让启动断言静默失效（名字永远匹配不上 ⇒ 永远不报错，
// 而检查看起来还在）。
const (
	preHookAccess     = "access"
	preHookModeration = "moderation"
	preHookAdmin      = "admin"
)

// inboundPreHookOrder 是引擎级入站 pre 钩子的**执行顺序契约**。
//
// 顺序不是实现细节，是行为——引擎按注册顺序执行：
//
//	access     名单命中即整条丢弃，必须最先。排到审查之后就会先建会话、先留上下文，
//	           本该被彻底丢弃的人反而留下了痕迹。
//	moderation 入站审查 / 黑名单 / 防刷。拦截的事件不进入任何路由。
//	admin      管理命令。未知命令必须能落回普通路由，别把聊天里的斜杠吃掉。
//
// 把契约写成**数据**而不是注释，才能被 checkPreHookOrder 断言：
// 只写在注释里的顺序约束，改错了照样编译、照样过测试。
var inboundPreHookOrder = []string{preHookAccess, preHookModeration, preHookAdmin}

// checkPreHookOrder 断言实际注册顺序符合 inboundPreHookOrder。
//
// 只比较契约里出现过的名字：pre 钩子是**条件注册**的（没有超管就没有 admin，
// 审查关闭就没有 moderation），所以实际序列通常是契约的子序列。
// 未命名的钩子被忽略——它们不参与契约。
//
// 只有一个契约名都没匹配上时算失败：那说明装配改了名字而契约没跟上，
// 此时"顺序正确"是空话——检查看起来还在，实际永远不报错。
// 返回的错误指出第一处逆序，便于直接定位。
func checkPreHookOrder(observed []string) error {
	rank := make(map[string]int, len(inboundPreHookOrder))
	for i, name := range inboundPreHookOrder {
		rank[name] = i
	}

	last, lastName, matched := -1, "", 0
	for _, name := range observed {
		if name == "" {
			continue
		}
		pos, known := rank[name]
		if !known {
			// 不在契约里的钩子不算违规：契约只约束它声明过的那些。
			continue
		}
		matched++
		if pos < last {
			return fmt.Errorf("pre 钩子 %q 注册在 %q 之后，违反执行顺序契约 [%s]",
				name, lastName, strings.Join(inboundPreHookOrder, " > "))
		}
		last, lastName = pos, name
	}
	if matched == 0 {
		return fmt.Errorf("契约 [%s] 里的钩子一个都没注册：装配的名字与契约脱节了，"+
			"这条检查已经形同虚设", strings.Join(inboundPreHookOrder, " > "))
	}
	return nil
}
