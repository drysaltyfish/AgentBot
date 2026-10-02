package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func Test_F53_DefaultTableIsValid(t *testing.T) {
	t.Parallel()
	p, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: actual=%v expected=nil", err)
	}
	if len(p.Actions()) == 0 {
		t.Fatalf("default table has no actions")
	}
	roles := p.Roles()
	want := []string{"admin", "everyone", "member", "owner", "superuser"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles: actual=%v expected=%v", roles, want)
	}
}

func Test_F53_UndefinedActionIsRejectedAtStartup(t *testing.T) {
	t.Parallel()
	bad := []byte(`
actions:
  send_msg:
    desc: 发送
    params: text
config:
  admin:
    - send_msg
    - not_defined_action
`)
	_, err := Load(bad)
	if !errors.Is(err, ErrUndefinedAction) {
		t.Fatalf("Load: actual=%v expected=ErrUndefinedAction", err)
	}
	if !strings.Contains(err.Error(), "not_defined_action") {
		t.Fatalf("error should name the offending action: %v", err)
	}

	if _, err := Load([]byte("config:\n  admin: []\n")); !errors.Is(err, ErrNoActions) {
		t.Fatalf("empty actions: actual=%v expected=ErrNoActions", err)
	}
}

func Test_F53_RenderIsStableAndContainsMandatorySentence(t *testing.T) {
	t.Parallel()
	p, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	first, err := p.Render("admin")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := p.Render("admin")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if first != second {
		t.Fatalf("render is not byte-stable")
	}
	if !strings.Contains(first, MandatorySentence) {
		t.Fatalf("mandatory sentence missing from prompt:\n%s", first)
	}
	if !strings.HasPrefix(first, "| 功能 | action | params | data |") {
		t.Fatalf("table header missing:\n%s", first)
	}
	if !strings.Contains(first, "| 发送消息 | send_msg |") {
		t.Fatalf("admin should be allowed to send_msg:\n%s", first)
	}
	if strings.Contains(first, "set_group_ban") {
		t.Fatalf("admin must not see set_group_ban:\n%s", first)
	}
}

func Test_F53_RenderRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	if _, err := p.Render("nobody"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("Render: actual=%v expected=ErrUnknownRole", err)
	}
}

func Test_F54_AllowIsCachedOnFirstMiss(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	if p.CacheFills() != 0 {
		t.Fatalf("cache should start empty: actual=%d", p.CacheFills())
	}
	if !p.Allow("admin", "send_msg") {
		t.Fatalf("admin should be allowed send_msg")
	}
	if p.CacheFills() != 1 {
		t.Fatalf("cache fills after first lookup: actual=%d expected=1", p.CacheFills())
	}
	for i := 0; i < 100; i++ {
		_ = p.Allow("admin", "send_msg")
	}
	if p.CacheFills() != 1 {
		t.Fatalf("cache was rebuilt on hits: actual=%d expected=1", p.CacheFills())
	}
}

func Test_F54_UnknownRoleIsFailClosed(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	if p.Allow("nonexistent-role", "send_msg") {
		t.Fatalf("unknown role must be denied (fail-closed)")
	}
	if p.Allow("", "send_msg") {
		t.Fatalf("empty role must be denied")
	}
	if p.Allow("admin", "not_an_action") {
		t.Fatalf("undefined action must be denied")
	}
}

func Test_F54_InvalidateRebuildsCache(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	_ = p.Allow("admin", "send_msg")
	if p.CacheFills() != 1 {
		t.Fatalf("setup: fills=%d", p.CacheFills())
	}
	p.Invalidate()
	_ = p.Allow("admin", "send_msg")
	if p.CacheFills() != 2 {
		t.Fatalf("cache not rebuilt after Invalidate: actual=%d expected=2", p.CacheFills())
	}
}

func Test_F54_ConcurrentAllowIsRaceFree(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = p.Allow("admin", "send_msg")
				_ = p.Allow("member", "set_group_ban")
				_, _ = p.Render("owner")
			}
		}()
	}
	wg.Wait()
	if p.Allow("member", "set_group_ban") {
		t.Fatalf("member must not be allowed set_group_ban")
	}
}

type fakeLookup struct {
	role string
	err  error
}

func (f fakeLookup) MemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.role, nil
}

func Test_F53_RoleResolverIsFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	super := NewRoleResolver([]int64{42}, fakeLookup{role: "member"})
	if got := super.Resolve(ctx, 1, 42); got != RoleSuperUser {
		t.Fatalf("superuser: actual=%q expected=%q", got, RoleSuperUser)
	}
	if got := super.Resolve(ctx, 1, 7); got != RoleMember {
		t.Fatalf("member: actual=%q expected=%q", got, RoleMember)
	}

	admin := NewRoleResolver(nil, fakeLookup{role: "admin"})
	if got := admin.Resolve(ctx, 1, 7); got != RoleAdmin {
		t.Fatalf("admin: actual=%q", got)
	}
	owner := NewRoleResolver(nil, fakeLookup{role: "owner"})
	if got := owner.Resolve(ctx, 1, 7); got != RoleOwner {
		t.Fatalf("owner: actual=%q", got)
	}

	// 查询失败、无 lookup、无群号、未知角色 → everyone
	failing := NewRoleResolver(nil, fakeLookup{err: errors.New("timeout")})
	if got := failing.Resolve(ctx, 1, 7); got != RoleEveryone {
		t.Fatalf("lookup error: actual=%q expected=%q", got, RoleEveryone)
	}
	noLookup := NewRoleResolver(nil, nil)
	if got := noLookup.Resolve(ctx, 1, 7); got != RoleEveryone {
		t.Fatalf("nil lookup: actual=%q expected=%q", got, RoleEveryone)
	}
	if got := admin.Resolve(ctx, 0, 7); got != RoleEveryone {
		t.Fatalf("private chat: actual=%q expected=%q", got, RoleEveryone)
	}
	weird := NewRoleResolver(nil, fakeLookup{role: "wizard"})
	if got := weird.Resolve(ctx, 1, 7); got != RoleEveryone {
		t.Fatalf("unknown role: actual=%q expected=%q", got, RoleEveryone)
	}
	var nilResolver *RoleResolver
	if got := nilResolver.Resolve(ctx, 1, 7); got != RoleEveryone {
		t.Fatalf("nil resolver: actual=%q expected=%q", got, RoleEveryone)
	}
}

func Test_F53_ResolvedRoleFeedsAllow(t *testing.T) {
	t.Parallel()
	p, _ := LoadDefault()
	resolver := NewRoleResolver([]int64{42}, fakeLookup{role: "member"})
	role := resolver.Resolve(context.Background(), 1, 42)
	if !p.Allow(role, "set_group_ban") {
		t.Fatalf("resolved superuser role should be allowed to ban")
	}
	role = resolver.Resolve(context.Background(), 1, 7)
	if p.Allow(role, "set_group_ban") {
		t.Fatalf("resolved member role must not be allowed to ban")
	}
}
