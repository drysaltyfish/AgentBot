package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/toggle"
)

// switchCommand 处理 /switch <plugin> on|off（F-19）。
//
// 只在群里生效（开关按群隔离），且限 owner/admin——否则任何群成员都能把功能关掉。
func switchCommand(c *router.Ctx, toggles *toggle.Toggle, sender *outbound.Sender) {
	target := outbound.PrivateTarget(c.Event.UserID)
	if c.Event.GroupID != 0 {
		target = outbound.GroupTarget(c.Event.GroupID)
	}
	reply := func(msg string) {
		if _, err := sender.SendMany(context.Background(), target, []string{msg}, 0); err != nil {
			// 回执失败无处可报，交回路由层的日志即可。
			return
		}
	}

	if c.Event.GroupID == 0 {
		reply("这个开关按群生效，请在群里使用。")
		return
	}
	if role := agentRole(c.Event); role != agent.RoleOwner && role != agent.RoleAdmin {
		reply("只有群管理可以改这个开关。")
		return
	}

	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.MessageString()), "/switch"))
	args, err := router.ParseCommandArgs(raw)
	if err != nil || len(args) != 2 {
		reply("用法：/switch <plugin> on|off")
		return
	}
	plugin, action := args[0], strings.ToLower(args[1])
	if action != "on" && action != "off" {
		reply("用法：/switch <plugin> on|off")
		return
	}
	if err := toggles.Set(plugin, c.Event.GroupID, action == "on"); err != nil {
		reply(fmt.Sprintf("改不了：%v", err))
		return
	}
	reply(fmt.Sprintf("已把 %s 在本群设为 %s。", plugin, action))
}
