package config

import (
	"strings"
	"testing"
)

// bptr 是本文件用到的指针辅助（配置用 *T 区分未设置与零值）。
func bptr(v bool) *bool { return &v }

// Test_F58_AccessValidation 覆盖名单配置的 fail-fast：
// 启用却不写模式、allow 模式两个名单都空、角色名写错、关掉超管绕过却没配超管。
func Test_F58_AccessValidation(t *testing.T) {
	t.Parallel()
	base := func() *Config {
		c := Default()
		c.LLM.Model = "m"
		c.Transport.Mode = "wsclient"
		c.Transport.URL = "ws://127.0.0.1:1"
		c.Access.Enabled = bptr(true)
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"合法 allow", func(c *Config) {
			c.Access.Mode = "allow"
			c.Access.Users = []int64{100}
		}, ""},
		{"合法 deny（名单可空）", func(c *Config) {
			c.Access.Mode = "deny"
		}, ""},
		{"启用但未写模式", func(c *Config) {
			c.Access.Users = []int64{100}
		}, "access.mode"},
		{"模式写错", func(c *Config) {
			c.Access.Mode = "banana"
		}, "access.mode"},
		{"allow 且两个名单都空", func(c *Config) {
			c.Access.Mode = "allow"
		}, "access.users"},
		{"角色名写错", func(c *Config) {
			c.Access.Mode = "deny"
			c.Access.Roles = map[string][]int64{"boss": {1}}
		}, "access.roles"},
		{"关掉超管绕过却没有超管", func(c *Config) {
			c.Access.Mode = "deny"
			c.Access.BypassSuperUsers = bptr(false)
		}, "access.bypass_super_users"},
		{"关掉超管绕过但有超管（放行）", func(c *Config) {
			c.Access.Mode = "deny"
			c.Access.BypassSuperUsers = bptr(false)
			c.Access.Roles = map[string][]int64{"SuperUser": {1}}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过校验，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望校验失败（含 %s），实际通过", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息应提到 %s，实际: %v", tc.wantErr, err)
			}
		})
	}
}

// Test_F58_AccessDefaults 钉住默认值：关闭、模式 off、群内看人、超管绕过、不刷日志。
func Test_F58_AccessDefaults(t *testing.T) {
	t.Parallel()
	var a Access
	if a.EffectiveEnabled() {
		t.Fatal("默认应关闭")
	}
	if a.EffectiveMode() != "off" {
		t.Fatalf("默认模式应为 off: %q", a.EffectiveMode())
	}
	if !a.EffectiveCheckUsersInGroup() {
		t.Fatal("默认应在群里也应用用户名单")
	}
	if !a.EffectiveBypassSuperUsers() {
		t.Fatal("默认应让超管绕过名单")
	}
	if a.EffectiveLogDrops() {
		t.Fatal("默认不应逐条记录丢弃日志")
	}
	// 启用但模式为空：归一为 off（校验会拦下这种配置，这里只锁归一行为）。
	on := Access{Enabled: bptr(true)}
	if on.EffectiveMode() != "off" {
		t.Fatalf("启用但未写模式应归一为 off: %q", on.EffectiveMode())
	}
}

// Test_F58_LegacySuperUsersKeyIsRejected 钉住破坏性变更的迁移路径：
// 旧键 moderation.super_users 已移除，出现时必须**报错并指路**，
// 而不是被静默忽略（那会表现成"我明明是超管，命令却不管用"）。
func Test_F58_LegacySuperUsersKeyIsRejected(t *testing.T) {
	t.Parallel()
	raw := []byte("transport:\n  mode: wsclient\n  url: ws://127.0.0.1:1\nllm:\n  model: m\nmoderation:\n  super_users: [12345]\n")
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Moderation.LegacySuperUsers) != 1 || cfg.Moderation.LegacySuperUsers[0] != 12345 {
		t.Fatalf("旧键应被解出以便报错: %+v", cfg.Moderation.LegacySuperUsers)
	}
	verr := cfg.Validate()
	if verr == nil {
		t.Fatal("旧键必须让校验失败")
	}
	msg := verr.Error()
	for _, want := range []string{"moderation.super_users", "access.roles.superuser", "12345"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q，实际: %v", want, msg)
		}
	}
}

// Test_F58_SuperUsersLiveInAccessRoles 确认新位置可用且旧字段不参与任何生效值。
func Test_F58_SuperUsersLiveInAccessRoles(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.LLM.Model = "m"
	cfg.Transport.Mode = "wsclient"
	cfg.Transport.URL = "ws://127.0.0.1:1"
	cfg.Access.Roles = map[string][]int64{"superuser": {7}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(cfg.Moderation.LegacySuperUsers) != 0 {
		t.Fatal("新配置不应触发旧键")
	}
}

// Test_F58_SuperUserZeroDoesNotSatisfyTheFailClosedGuard 钉住一处守卫绕过。
//
// `access.bypass_super_users: false` 的守卫是"关掉绕过就必须真的有人能管理"。
// 判定曾经在这里自己扫一遍 map、只数个数（len(ids) > 0），而真正的解析
// （internal/access.NewRoles）会丢弃 QQ 0——两边不一致，于是 superuser: [0]
// 让校验通过、运行时却一个超管都没有：名单一旦配错就再也无人能管。
//
// 判定必须复用 internal/access 的解析，两处规则不能再各写一遍。
func Test_F58_SuperUserZeroDoesNotSatisfyTheFailClosedGuard(t *testing.T) {
	t.Parallel()

	build := func(ids []int64) *Config {
		cfg := Default()
		cfg.LLM.Model = "m"
		cfg.Transport.Mode = "wsclient"
		cfg.Transport.URL = "ws://127.0.0.1:1"
		bypass := false
		cfg.Access.BypassSuperUsers = &bypass
		cfg.Access.Enabled = bptr(true)
		cfg.Access.Mode = "allow"
		cfg.Access.Users = []int64{10001}
		cfg.Access.Roles = map[string][]int64{"superuser": ids}
		return cfg
	}

	// 只有 QQ 0 不是"有超管"：它会被解析层丢弃。
	verr := build([]int64{0}).Validate()
	if verr == nil {
		t.Fatal("superuser: [0] 必须让校验失败——解析层会丢弃 0，实际没有任何超管")
	}
	if !strings.Contains(verr.Error(), "access.bypass_super_users") {
		t.Fatalf("错误应指向 access.bypass_super_users，实际: %v", verr)
	}

	// 混着写一个真实 QQ 号则应当通过。
	if err := build([]int64{0, 10001}).Validate(); err != nil {
		t.Fatalf("有真实超管时应校验通过: %v", err)
	}
}
