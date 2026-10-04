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
