package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/access"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// accessEvent 造一条群消息事件（user/group/role 可控）。
func accessEvent(t *testing.T, userID, groupID int64, role string) *event.Event {
	t.Helper()
	raw := fmt.Sprintf(
		`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":%d,"group_id":%d,"message_id":1,"sender":{"user_id":%d,"role":%q},"message":[{"type":"text","data":{"text":"hi"}}]}`,
		userID, groupID, userID, role)
	return event.NewEvent([]byte(raw))
}

// accessEngine 造一个"只有一条兜底路由"的引擎，返回引擎与命中计数器。
func accessEngine(rule router.Rule) (*router.Engine, *int) {
	routes := router.NewRouter()
	engine := router.NewEngine(routes)
	if rule != nil {
		engine.UsePre(rule)
	}
	hits := 0
	routes.OnMessage().Handle(func(*router.Ctx) { hits++ })
	return engine, &hits
}

// Test_F58_AccessRuleDropsUnlistedAtRouting 是这次扩展的核心验收：
// 名单外的消息必须在**路由层就被丢弃**——兜底路由都不执行，计数为 0。
func Test_F58_AccessRuleDropsUnlistedAtRouting(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Access.Enabled = ptr(true)
	cfg.Access.Mode = "allow"
	cfg.Access.Users = []int64{100}
	cfg.Access.Groups = []int64{900}
	controls := buildAccessControls(cfg, testLogger(t))
	supers := superUsersFrom(cfg, controls.Roles)
	cat := metrics.NewCatalog(metrics.CatalogOptions{})

	engine, hits := accessEngine(accessRule(controls, supers, cat, nil, testLogger(t)))

	if n := engine.Dispatch(context.Background(), accessEvent(t, 100, 900, "member"), nil); n != 1 {
		t.Fatalf("白名单内的消息应被处理: matched=%d", n)
	}
	if n := engine.Dispatch(context.Background(), accessEvent(t, 555, 900, "member"), nil); n != 0 {
		t.Fatalf("名单外用户的消息不应进入任何路由: matched=%d", n)
	}
	if n := engine.Dispatch(context.Background(), accessEvent(t, 100, 777, "member"), nil); n != 0 {
		t.Fatalf("名单外群的消息不应进入任何路由: matched=%d", n)
	}
	if *hits != 1 {
		t.Fatalf("兜底路由只应执行一次: hits=%d", *hits)
	}

	var buf bytes.Buffer
	if err := cat.Registry.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	// 两条被丢弃的消息应各计一次。
	if !strings.Contains(buf.String(), "events_dropped_total{reason=\"access\"} 2") {
		t.Fatalf("丢弃应计入 events_dropped 指标:\n%s", buf.String())
	}
}

// Test_F58_DenyModeDropsListedOnly 覆盖黑名单：名单内丢弃，名单外照常处理。
func Test_F58_DenyModeDropsListedOnly(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Access.Enabled = ptr(true)
	cfg.Access.Mode = "deny"
	cfg.Access.Users = []int64{666}
	controls := buildAccessControls(cfg, testLogger(t))
	supers := superUsersFrom(cfg, controls.Roles)

	engine, hits := accessEngine(accessRule(controls, supers, nil, nil, testLogger(t)))
	if n := engine.Dispatch(context.Background(), accessEvent(t, 666, 900, "member"), nil); n != 0 {
		t.Fatalf("黑名单用户不应进入路由: matched=%d", n)
	}
	if n := engine.Dispatch(context.Background(), accessEvent(t, 777, 900, "member"), nil); n != 1 {
		t.Fatalf("名单外用户应照常处理: matched=%d", n)
	}
	if *hits != 1 {
		t.Fatalf("hits=%d, want 1", *hits)
	}
}

// Test_F58_SuperUsersBypassTheList 钉住"配错白名单不会把自己锁在门外"。
func Test_F58_SuperUsersBypassTheList(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Access.Enabled = ptr(true)
	cfg.Access.Mode = "allow"
	cfg.Access.Users = []int64{100}
	cfg.Access.Roles = map[string][]int64{"superuser": {42}}
	controls := buildAccessControls(cfg, testLogger(t))
	supers := superUsersFrom(cfg, controls.Roles)

	engine, _ := accessEngine(accessRule(controls, supers, nil, nil, testLogger(t)))
	if n := engine.Dispatch(context.Background(), accessEvent(t, 42, 900, "member"), nil); n != 1 {
		t.Fatalf("超管应绕过名单: matched=%d", n)
	}
	if n := engine.Dispatch(context.Background(), accessEvent(t, 43, 900, "member"), nil); n != 0 {
		t.Fatalf("非超管仍应被名单拦住: matched=%d", n)
	}
}

// Test_F58_AccessDisabledIsNoop 钉住默认关闭：不启用名单时行为与之前完全一致。
func Test_F58_AccessDisabledIsNoop(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	controls := buildAccessControls(cfg, testLogger(t))
	if controls.Policy.Active() {
		t.Fatal("默认不应启用名单")
	}
	engine, _ := accessEngine(accessRule(controls, nil, nil, nil, testLogger(t)))
	if n := engine.Dispatch(context.Background(), accessEvent(t, 1, 2, "member"), nil); n != 1 {
		t.Fatalf("未启用名单时应照常处理: matched=%d", n)
	}
}

// Test_F58_RoleForEventPrefersExplicitRoles 覆盖"用 QQ 号指定角色"的优先级：
// 显式指定 > 超管名单 > 平台角色；显式指定还能把平台 owner 降级为 member。
func Test_F58_RoleForEventPrefersExplicitRoles(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Access.Roles = map[string][]int64{
		"superuser": {1},
		"owner":     {2},
		"admin":     {3},
		"member":    {9},
	}
	controls := buildAccessControls(cfg, testLogger(t))
	supers := superUsersFrom(cfg, controls.Roles)

	if got := roleForEvent(accessEvent(t, 1, 5, "member"), controls, supers); got != policy.RoleSuperUser {
		t.Fatalf("显式超管: %q", got)
	}
	if got := roleForEvent(accessEvent(t, 2, 5, "member"), controls, supers); got != policy.RoleOwner {
		t.Fatalf("显式 owner: %q", got)
	}
	if got := roleForEvent(accessEvent(t, 3, 5, "member"), controls, supers); got != policy.RoleAdmin {
		t.Fatalf("显式 admin: %q", got)
	}
	// 显式 member 覆盖平台上报的 owner：部署者的指定优先。
	if got := roleForEvent(accessEvent(t, 9, 5, "owner"), controls, supers); got != policy.RoleMember {
		t.Fatalf("显式 member 应覆盖平台 owner: %q", got)
	}
	// 未显式指定时看平台角色。
	if got := roleForEvent(accessEvent(t, 77, 5, "owner"), controls, supers); got != policy.RoleOwner {
		t.Fatalf("平台 owner: %q", got)
	}
	if got := roleForEvent(accessEvent(t, 77, 5, "member"), controls, supers); got != policy.RoleMember {
		t.Fatalf("平台 member: %q", got)
	}
}

// Test_F58_AgentRoleForMapsSuperUserToOwner 钉住两套角色名空间的映射：
// agent.Role 没有 superuser 一档，超管按最高权限的 owner 处理。
func Test_F58_AgentRoleForMapsSuperUserToOwner(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Access.Roles = map[string][]int64{"superuser": {1}}
	controls := buildAccessControls(cfg, testLogger(t))
	supers := superUsersFrom(cfg, controls.Roles)

	cases := []struct {
		userID int64
		role   string
		want   agent.Role
	}{
		{1, "member", agent.RoleOwner},
		{2, "admin", agent.RoleAdmin},
		{3, "member", agent.RoleMember},
	}
	for _, tc := range cases {
		if got := agentRoleFor(accessEvent(t, tc.userID, 5, tc.role), controls, supers); got != tc.want {
			t.Fatalf("user=%d 平台=%s: agentRoleFor=%q, want %q", tc.userID, tc.role, got, tc.want)
		}
	}
}

// Test_F58_SuperUsersFromUnionsBothSources 钉住"超管只有一份名单"：
// moderation.super_users 与 access.roles.superuser 取并集。
func Test_F58_SuperUsersFromUnionsBothSources(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Moderation.SuperUsers = []int64{10}
	roles := access.NewRoles(map[string][]int64{"superuser": {20}})
	got := superUsersFrom(cfg, roles)
	if len(got) != 2 {
		t.Fatalf("超管并集应为 2 个: %v", got)
	}
	if _, ok := got[10]; !ok {
		t.Fatalf("moderation.super_users 应计入: %v", got)
	}
	if _, ok := got[20]; !ok {
		t.Fatalf("access.roles.superuser 应计入: %v", got)
	}
}
