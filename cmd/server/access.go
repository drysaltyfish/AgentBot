package main

import (
	"github.com/drysaltyfish/agentbot/internal/access"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// accessControls 汇总名单判定与"QQ 号 → 角色"的显式指定。
type accessControls struct {
	Policy           *access.Policy
	Roles            *access.Roles
	BypassSuperUsers bool
	LogDrops         bool
}

// buildAccessControls 按配置构造名单与角色；两者都默认关闭/为空。
func buildAccessControls(cfg *config.Config, lg *observe.Logger) *accessControls {
	controls := &accessControls{
		Policy: access.NewPolicy(cfg.Access.EffectiveMode(), access.Listed{
			Users:  cfg.Access.Users,
			Groups: cfg.Access.Groups,
		}, cfg.Access.EffectiveCheckUsersInGroup()),
		Roles:            access.NewRoles(cfg.Access.Roles),
		BypassSuperUsers: cfg.Access.EffectiveBypassSuperUsers(),
		LogDrops:         cfg.Access.EffectiveLogDrops(),
	}
	if lg != nil {
		lg.Component("access").Info("access control initialized",
			"mode", controls.Policy.Summary(),
			"check_users_in_group", cfg.Access.EffectiveCheckUsersInGroup(),
			"bypass_super_users", controls.BypassSuperUsers,
			"explicit_roles", controls.Roles.Len())
	}
	return controls
}

// superUsersFrom 返回"谁说了算"的**唯一**名单：access.roles.superuser。
//
// 管理命令授权、权限判定、名单绕过、never-ban 都读这一份。配置里只有这一个位置
// （旧的 moderation.super_users 已移除，出现即启动失败并给出迁移指引）。
func superUsersFrom(roles *access.Roles) map[int64]struct{} {
	ids := roles.SuperUsers()
	out := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// accessRule 是挂在引擎最前面的 pre 钩子：名单外的消息**直接丢弃**。
//
// 丢弃意味着不进入路由匹配、不建会话、不落库、不产生模型调用——这正是"路由层抛弃"。
// 返回 false 即终止本次分发；审计与指标照记，便于事后解释"为什么它没回我"。
func accessRule(controls *accessControls, supers map[int64]struct{}, cat *metrics.Catalog, alog *audit.Logger, lg *observe.Logger) router.Rule {
	return func(c *router.Ctx) bool {
		if c == nil || c.Event == nil || controls == nil || !controls.Policy.Active() {
			return true
		}
		userID, groupID := c.Event.UserID, c.Event.GroupID
		if controls.BypassSuperUsers {
			if _, ok := supers[userID]; ok {
				return true
			}
		}
		if controls.Policy.Allow(userID, groupID) {
			return true
		}
		reason := "not-in-allow-list"
		if controls.Policy.Mode() == access.ModeDeny {
			reason = "in-deny-list"
		}
		if cat != nil {
			cat.EventsDropped.With(metrics.Labels{"reason": "access"}).Inc()
		}
		if alog != nil {
			alog.Log(audit.Event{
				Type:   audit.EventInboundBlocked,
				UserID: userID, GroupID: groupID,
				Action: "access." + string(controls.Policy.Mode()),
				Result: audit.ResultDenied,
				Params: map[string]string{"reason": reason},
			})
		}
		if controls.LogDrops && lg != nil {
			lg.Component("access").Info("message dropped by access list",
				"reason", reason, "user_id", userID, "group_id", groupID)
		}
		return false
	}
}

// roleForEvent 返回事件的**策略角色**（policy 的角色名空间）：
// access.roles 显式指定 > 超管名单 > 平台上报的群成员角色 > everyone。
//
// 显式指定优先是有意的：平台角色描述"他在这个群里的身份"，
// 而 access.roles 描述"部署者认定他是谁"——两者冲突时以后者为准。
func roleForEvent(ev *event.Event, controls *accessControls, supers map[int64]struct{}) string {
	if ev == nil {
		return policy.RoleEveryone
	}
	if controls != nil {
		if role, ok := controls.Roles.Lookup(ev.UserID); ok {
			return role
		}
	}
	if _, ok := supers[ev.UserID]; ok {
		return policy.RoleSuperUser
	}
	switch agentRole(ev) {
	case agent.RoleOwner:
		return policy.RoleOwner
	case agent.RoleAdmin:
		return policy.RoleAdmin
	case agent.RoleMember, agent.RolePrivate:
		return policy.RoleMember
	default:
		return policy.RoleEveryone
	}
}

// agentRoleFor 返回 F-45 审批闸门使用的 agent 角色；显式指定同样优先。
//
// 超管映射为 owner：审批表里 owner 是权限最高的一档，而 agent.Role 没有
// superuser 这一档（两套角色名空间不同，映射关系在此显式写出）。
func agentRoleFor(ev *event.Event, controls *accessControls, supers map[int64]struct{}) agent.Role {
	switch roleForEvent(ev, controls, supers) {
	case policy.RoleSuperUser, policy.RoleOwner:
		return agent.RoleOwner
	case policy.RoleAdmin:
		return agent.RoleAdmin
	case policy.RoleMember:
		if ev != nil && ev.GroupID == 0 {
			return agent.RolePrivate
		}
		return agent.RoleMember
	default:
		return agentRole(ev)
	}
}
