package main

import (
	"strings"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/reply"
	"github.com/drysaltyfish/agentbot/internal/router"
)

func stringOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// replyRule 把 behavior 配置翻译成路由谓词。
//
// 未配置时的默认：私聊 always、群聊 on_mention（群里不 @ 就不回复，避免刷屏），
// 且绝不回复机器人自己。
func replyRule(cfg *config.Config) router.Rule {
	private := cfg.Behavior.EffectivePrivate()
	group := cfg.Behavior.EffectiveGroup()
	atMe := router.AtMe()
	return func(c *router.Ctx) bool {
		if c.Event == nil || c.Event.UserID == 0 || c.Event.UserID == c.Event.SelfID {
			return false
		}
		if c.Event.GroupID == 0 {
			return private == config.ReplyAlways
		}
		switch group {
		case config.ReplyAlways:
			return true
		case config.ReplyOnMention:
			return atMe(c)
		default:
			return false
		}
	}
}

// promptSnapshotKeep 是每个会话保留的提示词快照条数（F-89 的环形保留）。
//
// 200 轮足够回溯"前缀是从哪一轮开始不稳的"，而快照本身只存指纹，体积很小。
const promptSnapshotKeep = 200

func floatOr(p *float64, fallback float64) float64 {
	if p != nil {
		return *p
	}
	return fallback
}

// groupScopedUserID 返回群聊里用于标识发言人的 QQ 号；私聊返回 0。
//
// **锚点是 QQ 号而不是昵称**：昵称随时会改，一改模型就认不出是同一个人。
// 私聊不标：只有两个人，每句都标是纯噪声。
func groupScopedUserID(userID, groupID int64) int64 {
	if groupID == 0 || userID <= 0 {
		return 0
	}
	return userID
}

// speakerDisplayName 返回群聊里展示用的名字（群名片优先）。
//
// 只用于**可读性**：身份判定始终靠 QQ 号，所以昵称怎么改都不会认错人。
func speakerDisplayName(sender event.Sender, groupID int64) string {
	if groupID == 0 {
		return ""
	}
	if name := strings.TrimSpace(sender.Card); name != "" {
		return name
	}
	return strings.TrimSpace(sender.Nickname)
}

// agentRole 把平台上报的成员角色映射成 F-45 的权限角色。
func agentRole(ev *event.Event) agent.Role {
	if ev.GroupID == 0 {
		return agent.RolePrivate
	}
	switch ev.Sender.Role {
	case "owner":
		return agent.RoleOwner
	case "admin":
		return agent.RoleAdmin
	default:
		return agent.RoleMember
	}
}

// sendShapeOf 从配置读取发送形态，缺省值与 config.Default 保持一致。
func sendShapeOf(cfg *config.Config) reply.Shape {
	return reply.Shape{
		SplitOnBlank: cfg.Behavior.EffectiveSplitOnBlankLine(),
		Delay:        cfg.Behavior.EffectiveSplitDelay(),
		MaxSegments:  cfg.Behavior.EffectiveMaxSegments(),
	}
}
