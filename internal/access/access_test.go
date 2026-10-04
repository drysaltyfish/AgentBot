package access

import (
	"testing"

	"github.com/drysaltyfish/agentbot/internal/policy"
)

// Test_F58_AccessListDecide 覆盖名单判定的全部组合：
// 私聊只看人、群聊看群 + 人（check_users_in_group 控制人名单是否在群里生效）。
func Test_F58_AccessListDecide(t *testing.T) {
	t.Parallel()
	users := []int64{100, 200}
	groups := []int64{900}

	cases := []struct {
		name    string
		mode    string
		check   bool
		userID  int64
		groupID int64
		want    bool
	}{
		// 关闭：一律放行。
		{"off 私聊", "off", true, 555, 0, true},
		{"off 群聊", "off", true, 555, 777, true},
		// 白名单 + 私聊。
		{"allow 私聊命中", "allow", true, 100, 0, true},
		{"allow 私聊未命中", "allow", true, 555, 0, false},
		// 白名单 + 群聊（人在名单、群不在）。
		{"allow 人命中群未命中(需要群)", "allow", true, 100, 777, false},
		{"allow 人命中群未命中(不需要群)", "allow", false, 100, 777, true},
		// 白名单 + 群聊（群在名单、人不在）。
		{"allow 群命中人未命中(要人也)", "allow", true, 555, 900, false},
		{"allow 群命中人未命中(只要群)", "allow", false, 555, 900, true},
		{"allow 两者都命中", "allow", true, 100, 900, true},
		// 黑名单。
		{"deny 私聊命中", "deny", true, 100, 0, false},
		{"deny 私聊未命中", "deny", true, 555, 0, true},
		{"deny 群命中", "deny", true, 555, 900, false},
		{"deny 群内人被命中", "deny", true, 200, 777, false},
		{"deny 群里不看人", "deny", false, 200, 777, true},
		{"deny 都不命中", "deny", true, 555, 777, true},
		// 未知模式按 off（配置校验负责拦下未知值）。
		{"未知模式", "banana", true, 555, 0, true},
	}
	for _, tc := range cases {
		p := NewPolicy(tc.mode, Listed{Users: users, Groups: groups}, tc.check)
		if got := p.Allow(tc.userID, tc.groupID); got != tc.want {
			t.Fatalf("%s: Allow(%d,%d)=%v, want %v（mode=%s check=%v）",
				tc.name, tc.userID, tc.groupID, got, tc.want, tc.mode, tc.check)
		}
	}
}

// Test_F58_AccessListIgnoresZeroIDs 钉住"0 不是合法 QQ 号/群号"：
// 配置里误写 0 不应该让所有私聊或所有群都被命中。
func Test_F58_AccessListIgnoresZeroIDs(t *testing.T) {
	t.Parallel()
	p := NewPolicy("allow", Listed{Users: []int64{0, 100}, Groups: []int64{0, 900}}, true)
	if p.Allow(0, 0) {
		t.Fatal("0 用户号不应被视为白名单成员")
	}
	if p.Allow(555, 0) != false {
		t.Fatal("0 群号不应被视为白名单群")
	}
}

// Test_F58_RolesLookupAndPrecedence 覆盖"同一个 QQ 被写进多个角色"时按权限取高，
// 避免配置书写顺序决定权限。
func Test_F58_RolesLookupAndPrecedence(t *testing.T) {
	t.Parallel()
	r := NewRoles(map[string][]int64{
		"superuser": {1},
		"owner":     {2},
		"admin":     {3, 5},
		"member":    {4, 5},
		"banana":    {6},
	})
	if role, ok := r.Lookup(1); !ok || role != policy.RoleSuperUser {
		t.Fatalf("QQ=1 应为 superuser，实际 %q/%v", role, ok)
	}
	if role, ok := r.Lookup(3); !ok || role != policy.RoleAdmin {
		t.Fatalf("QQ=3 应为 admin，实际 %q/%v", role, ok)
	}
	// 同一个 QQ 同时在 admin 与 member：取权限高的 admin。
	if role, _ := r.Lookup(5); role != policy.RoleAdmin {
		t.Fatalf("冲突时应取权限更高的角色，实际 %q", role)
	}
	// 未知角色名被忽略：不能因为写错角色名就悄悄给出一个角色。
	if _, ok := r.Lookup(6); ok {
		t.Fatal("未知角色名不应生效")
	}
	if _, ok := r.Lookup(999); ok {
		t.Fatal("未配置的 QQ 不应有角色")
	}
	if got := r.SuperUsers(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("SuperUsers=%v, want [1]", got)
	}
	if r.Len() != 5 {
		t.Fatalf("显式角色数=%d, want 5（0 与未知角色名不计）", r.Len())
	}
}

// Test_F58_PolicyModeAndSummary 覆盖模式归一与启动日志用的摘要。
func Test_F58_PolicyModeAndSummary(t *testing.T) {
	t.Parallel()
	p := NewPolicy("DENY", Listed{Users: []int64{7}, Groups: []int64{9}}, true)
	if p.Mode() != ModeDeny || !p.Active() {
		t.Fatalf("模式应归一为 deny: %v active=%v", p.Mode(), p.Active())
	}
	if got := p.Summary(); got != "deny users=[7] groups=[9]" {
		t.Fatalf("Summary=%q", got)
	}
	if NewPolicy("", Listed{}, true).Active() {
		t.Fatal("空模式不应生效")
	}
}
