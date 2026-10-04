package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// policyTestCaller 记录是否真的转发到了传输层。
type policyTestCaller struct{ calls int }

func (c *policyTestCaller) Call(_ context.Context, _ transport.Request) (transport.Response, error) {
	c.calls++
	return transport.Response{RetCode: 0}, nil
}

// Test_F53_LoadPolicyFallsBackToBuiltin 覆盖 F-53 的"内置基线 + 外部覆盖"：
// 文件缺失用内置表（默认配置就指向不存在的 actions.yaml），解析失败则启动失败。
func Test_F53_LoadPolicyFallsBackToBuiltin(t *testing.T) {
	t.Parallel()
	lg := testLogger(t)

	cfg := config.Default()
	cfg.Policy.File = ""
	if p, err := loadPolicy(cfg, lg); err != nil || p == nil {
		t.Fatalf("空路径应加载内置表: (%v,%v)", p, err)
	}

	cfg.Policy.File = filepath.Join(t.TempDir(), "missing.yaml")
	if p, err := loadPolicy(cfg, lg); err != nil || p == nil {
		t.Fatalf("文件缺失应退回内置表: (%v,%v)", p, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("actions: {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg.Policy.File = bad
	if _, err := loadPolicy(cfg, lg); err == nil {
		t.Fatal("非法权限表必须让启动失败")
	}
}

// Test_F53_PolicyRolePrefersSuperUsers 钉住角色来源唯一：
// 超管名单优先于平台上报的角色，避免出现第二份"谁说了算"的名单。
func Test_F53_PolicyRolePrefersSuperUsers(t *testing.T) {
	t.Parallel()
	supers := map[int64]struct{}{7: {}}

	if got := roleForEvent(&event.Event{UserID: 7, Sender: event.Sender{Role: "member"}}, nil, supers); got != policy.RoleSuperUser {
		t.Fatalf("超管应映射为 superuser: %q", got)
	}
	cases := []struct {
		name string
		ev   *event.Event
		want string
	}{
		{"owner", &event.Event{GroupID: 1, UserID: 1, Sender: event.Sender{Role: "owner"}}, policy.RoleOwner},
		{"admin", &event.Event{GroupID: 1, UserID: 1, Sender: event.Sender{Role: "admin"}}, policy.RoleAdmin},
		{"member", &event.Event{GroupID: 1, UserID: 1, Sender: event.Sender{Role: ""}}, policy.RoleMember},
		{"private", &event.Event{UserID: 1}, policy.RoleMember},
		{"nil event", nil, policy.RoleEveryone},
	}
	for _, tc := range cases {
		if got := roleForEvent(tc.ev, nil, supers); got != tc.want {
			t.Fatalf("%s: policyRole=%q, want %q", tc.name, got, tc.want)
		}
	}
}

// Test_F53_MiddlewareDeniesUnauthorizedAction 是执行侧硬拦截的验收：
// 角色允许集合之外的 action 不得到达传输层。
func Test_F53_MiddlewareDeniesUnauthorizedAction(t *testing.T) {
	t.Parallel()
	pol, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	next := &policyTestCaller{}
	guarded := policyMiddleware(newPolicyState(pol), nil)(next)

	memberCtx := policy.WithRole(context.Background(), policy.RoleMember)
	if _, err := guarded.Call(memberCtx, transport.Request{Action: "send_msg"}); err != nil {
		t.Fatalf("member 的 send_msg 应放行: %v", err)
	}
	if next.calls != 1 {
		t.Fatalf("放行的动作应转发到下一层: %d", next.calls)
	}
	_, err = guarded.Call(memberCtx, transport.Request{Action: "set_group_ban"})
	if err == nil {
		t.Fatal("member 的 set_group_ban 必须被拒绝")
	}
	if next.calls != 1 {
		t.Fatalf("被拒绝的动作不得到达传输层: %d", next.calls)
	}
	if !strings.Contains(err.Error(), "set_group_ban") {
		t.Fatalf("拒绝错误应指明 action: %v", err)
	}

	adminCtx := policy.WithRole(context.Background(), policy.RoleAdmin)
	if _, err := guarded.Call(adminCtx, transport.Request{Action: "get_group_member_info"}); err != nil {
		t.Fatalf("admin 应允许查询群成员: %v", err)
	}
	if _, err := guarded.Call(adminCtx, transport.Request{Action: "set_group_ban"}); err == nil {
		t.Fatal("admin 的 set_group_ban 必须被拒绝（默认表里没有）")
	}
}

// Test_F53_MiddlewareFailsClosedWithoutRole 钉住 fail-closed：
// ctx 里没有角色时按 everyone 判定，而 everyone 只有 send_msg。
func Test_F53_MiddlewareFailsClosedWithoutRole(t *testing.T) {
	t.Parallel()
	pol, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	next := &policyTestCaller{}
	guarded := policyMiddleware(newPolicyState(pol), nil)(next)

	if _, err := guarded.Call(context.Background(), transport.Request{Action: "send_msg"}); err != nil {
		t.Fatalf("everyone 的 send_msg 应放行: %v", err)
	}
	if _, err := guarded.Call(context.Background(), transport.Request{Action: "delete_msg"}); err == nil {
		t.Fatal("无角色上下文时 delete_msg 必须被拒绝")
	}
}

// Test_F53_PromptProviderRendersRoleTable 是提示词侧的验收：
// 表格必须进提示词、含强制声明，且只列该角色允许的 action。
func Test_F53_PromptProviderRendersRoleTable(t *testing.T) {
	t.Parallel()
	pol, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	base := func(context.Context, session.Key) string { return "人格设定" }
	prov := policyPromptProvider(base, newPolicyState(pol), nil)

	ctx := policy.WithRole(context.Background(), policy.RoleMember)
	text := prov(ctx, session.Key{})
	if !strings.Contains(text, "人格设定") {
		t.Fatalf("人格段应保留: %q", text)
	}
	if !strings.Contains(text, policy.MandatorySentence) {
		t.Fatalf("必须包含强制声明: %q", text)
	}
	if !strings.Contains(text, "send_msg") {
		t.Fatalf("member 应看到 send_msg: %q", text)
	}
	if strings.Contains(text, "set_group_ban") {
		t.Fatalf("member 不应看到未授权的 action: %q", text)
	}

	anon := prov(context.Background(), session.Key{})
	if !strings.Contains(anon, policy.MandatorySentence) || strings.Contains(anon, "delete_msg") {
		t.Fatalf("无角色上下文应按 everyone 渲染: %q", anon)
	}
}

// Test_F53_PolicyStateSwapTakesEffect 覆盖 F-24 的"权限表"观察项：
// 换表后，中间件必须按新表判定（旧的允许/拒绝立即失效）。
func Test_F53_PolicyStateSwapTakesEffect(t *testing.T) {
	t.Parallel()
	base, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	state := newPolicyState(base)
	next := &policyTestCaller{}
	guarded := policyMiddleware(state, nil)(next)
	memberCtx := policy.WithRole(context.Background(), policy.RoleMember)

	if _, err := guarded.Call(memberCtx, transport.Request{Action: "delete_msg"}); err == nil {
		t.Fatal("默认表下 member 不该能撤回消息")
	}

	custom, err := policy.Load([]byte("actions:\n  delete_msg:\n    desc: 撤回消息\n    params: message_id\n    data: \"\"\nconfig:\n  member:\n    - delete_msg\n"))
	if err != nil {
		t.Fatalf("Load custom: %v", err)
	}
	state.Store(custom)
	if _, err := guarded.Call(memberCtx, transport.Request{Action: "delete_msg"}); err != nil {
		t.Fatalf("换表后 member 应能撤回消息: %v", err)
	}
	// 新表没有 send_msg：原来的放行必须立即失效（等价于 F-54 的缓存整体失效）。
	if _, err := guarded.Call(memberCtx, transport.Request{Action: "send_msg"}); err == nil {
		t.Fatal("新表没有 send_msg，必须立即拒绝")
	}
}
