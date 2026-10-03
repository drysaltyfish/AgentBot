package scoped

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePersona(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func Test_F82_ScopePathHelpers(t *testing.T) {
	if got := Parent("a/b/c"); got != "a/b" {
		t.Fatalf("Parent(a/b/c)=%q", got)
	}
	if got := Parent("a"); got != "" {
		t.Fatalf("Parent(a)=%q", got)
	}
	if got := Parent(""); got != "" {
		t.Fatalf("Parent(empty)=%q", got)
	}
	if got := CleanScope(" /a//b/ "); got != "a/b" {
		t.Fatalf("CleanScope=%q", got)
	}
	if got := UserScope("g", "u"); got != "group/g/user/u" {
		t.Fatalf("UserScope=%q", got)
	}
	if got := UserScope("", "u"); got != "user/u" {
		t.Fatalf("UserScope(no group)=%q", got)
	}
	chain := SessionScopes("p", "g", "u")
	want := []string{"group/g/user/u", "group/g", "p"}
	if len(chain) != len(want) {
		t.Fatalf("SessionScopes=%v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Fatalf("SessionScopes=%v, want %v", chain, want)
		}
	}
	if got := SessionScopes("p", "", ""); len(got) != 1 || got[0] != "p" {
		t.Fatalf("SessionScopes(persona only)=%v", got)
	}

	valid := []string{"x", "a1", "xiangcheng-niang", "a.b_c-d", "0"}
	for _, name := range valid {
		if !ValidPersonaName(name) {
			t.Fatalf("ValidPersonaName(%q)=false, want true", name)
		}
	}
	invalid := []string{"", "Bad", "-x", ".x", "_x", "a/b", "a b", "人格"}
	for _, name := range invalid {
		if ValidPersonaName(name) {
			t.Fatalf("ValidPersonaName(%q)=true, want false", name)
		}
	}
}

func Test_F82_LayeredOverride(t *testing.T) {
	c := NewConfig()
	c.Set("", "temp", 10)
	c.Set(GroupScope("g1"), "temp", 20)
	c.Set(UserScope("g1", "u1"), "temp", 30)

	cases := []struct {
		scope string
		want  int
	}{
		{"", 10},
		{GroupScope("g1"), 20},
		{UserScope("g1", "u1"), 30},
		{GroupScope("g2"), 10},
		{UserScope("g2", "u2"), 10},
	}
	for _, tc := range cases {
		got, err := Resolve[int](c, tc.scope, "temp")
		if err != nil {
			t.Fatalf("Resolve(scope=%q): %v", tc.scope, err)
		}
		if got != tc.want {
			t.Fatalf("Resolve(scope=%q)=%d, want %d", tc.scope, got, tc.want)
		}
	}

	// 人格作用域：x 覆盖全局，y 回退全局。
	c.Set("x", "temp", 99)
	if got, _ := c.Get("x", "temp"); got != 99 {
		t.Fatalf("persona x=%v, want 99", got)
	}
	if got, _ := c.Get("y", "temp"); got != 10 {
		t.Fatalf("persona y=%v, want 10", got)
	}
	// 会话优先级链：用户 → 群 → 人格 → 全局。
	if got, _ := c.GetScoped("temp", SessionScopes("x", "g1", "u1")...); got != 30 {
		t.Fatalf("session scoped (user)=%v, want 30", got)
	}
	if got, _ := c.GetScoped("temp", SessionScopes("x", "g2", "u2")...); got != 99 {
		t.Fatalf("session scoped (persona)=%v, want 99", got)
	}
	if got, _ := c.GetScoped("temp", SessionScopes("y", "g2", "u2")...); got != 10 {
		t.Fatalf("session scoped (global)=%v, want 10", got)
	}
	// GetScoped 只查自身层，不做父路径回退。
	if _, ok := c.GetScoped("temp", "group/g1/user/u1"); !ok {
		t.Fatal("GetScoped exact layer missed")
	}

	// 缺失键不静默返回零值。
	if _, ok := c.Get("", "nope"); ok {
		t.Fatal("Get missing key reported ok")
	}
}

func Test_F82_ExplicitZeroVsUnset(t *testing.T) {
	c := NewConfig()
	c.Set("", "retention", 10)
	c.Set(GroupScope("g1"), "retention", 0)

	v, ok := c.Get(GroupScope("g1"), "retention")
	if !ok {
		t.Fatal("explicit zero reported unset")
	}
	if v != 0 {
		t.Fatalf("explicit zero=%v, want 0", v)
	}
	if got, err := Resolve[int](c, UserScope("g1", "u1"), "retention"); err != nil || got != 0 {
		t.Fatalf("Resolve explicit zero=(%d,%v), want (0,nil)", got, err)
	}

	// 未设置的键必须 ok=false，哪怕全局有值也不能误报为显式 0。
	if _, ok := c.Get(GroupScope("g1"), "missing"); ok {
		t.Fatal("unset key reported ok")
	}

	// 显式 nil 也是"设置过"。
	c.Set(GroupScope("g1"), "note", nil)
	v, ok = c.Get(GroupScope("g1"), "note")
	if !ok || v != nil {
		t.Fatalf("explicit nil=(%v,%v), want (nil,true)", v, ok)
	}
}

func Test_F82_ScopeFileExplicitZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "group.yml")
	writePersona(t, dir, "group.yml", "ambient_token_budget: 0\nhistory_turns: 12\n")

	c := NewConfig()
	c.Set("", "ambient_token_budget", 1200)
	c.Set("", "history_turns", 20)
	if err := c.LoadFile(GroupScope("g1"), path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if v, ok := c.Get(GroupScope("g1"), "ambient_token_budget"); !ok || v != 0 {
		t.Fatalf("explicit zero lost: (%v,%v)", v, ok)
	}
	if v, ok := c.Get(GroupScope("g1"), "history_turns"); !ok || v != 12 {
		t.Fatalf("file value lost: (%v,%v)", v, ok)
	}

	// 解析失败必须原子：不修改任何层。
	bad := filepath.Join(dir, "bad.yml")
	writePersona(t, dir, "bad.yml", "history_turns: [unclosed\n")
	if err := c.LoadFile(GroupScope("g1"), bad); err == nil {
		t.Fatal("LoadFile(malformed) succeeded")
	}
	if v, _ := c.Get(GroupScope("g1"), "history_turns"); v != 12 {
		t.Fatalf("failed LoadFile mutated layer: %v", v)
	}
}

func Test_F82_ResolveErrors(t *testing.T) {
	c := NewConfig()
	c.Set("", "count", 3)
	c.Set("", "name", "bot")

	if got, err := Resolve[int](c, "", "count"); err != nil || got != 3 {
		t.Fatalf("Resolve count=(%d,%v)", got, err)
	}
	if _, err := Resolve[string](c, "", "count"); err == nil {
		t.Fatal("type mismatch did not error")
	} else if !strings.Contains(err.Error(), "类型") {
		t.Fatalf("type mismatch error unclear: %v", err)
	}
	if _, err := Resolve[int](c, "unknown", "missing"); err == nil {
		t.Fatal("unknown scope silently returned zero")
	}
	if got, err := Resolve[string](c, "unknown", "name"); err != nil || got != "bot" {
		t.Fatalf("fallback to global=(%q,%v)", got, err)
	}
}

func Test_F82_RouteKeyStable(t *testing.T) {
	base := RouteKey(GroupScope("g1"), "x")
	if base != RouteKey(GroupScope("g1"), "x") {
		t.Fatal("same input produced different keys")
	}
	if base == RouteKey(GroupScope("g1"), "y") {
		t.Fatal("different persona produced same key")
	}
	if base == RouteKey(GroupScope("g2"), "x") {
		t.Fatal("different scope produced same key")
	}
	if RouteKey(GroupScope("g1"), "") != RouteKey(GroupScope("g1"), DefaultPersona) {
		t.Fatal("empty persona key is not the default persona key")
	}
	if NormalizePersona("  ") != DefaultPersona {
		t.Fatal("blank persona is not normalized to default")
	}
	// 长度前缀消歧：不同切分不得撞键。
	if RouteKey("a/b", "c") == RouteKey("a", "b/c") {
		t.Fatal("route key is ambiguous")
	}
	if RouteFingerprint(GroupScope("g1"), "x") == "" {
		t.Fatal("empty route fingerprint")
	}
}

func Test_F82_PersonaLoadAndApply(t *testing.T) {
	dir := t.TempDir()
	writePersona(t, dir, "x.yml", "name: x\ndescription: 测试人格\nsystem_prompt: 你是 x\nconfig:\n  history_turns: 5\n")
	writePersona(t, dir, "default.yml", "name: default\nsystem_prompt: 外部默认\n")

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Len() != 2 {
		t.Fatalf("Len=%d, want 2", reg.Len())
	}
	p, ok := reg.Get("x")
	if !ok {
		t.Fatal("persona x not found")
	}
	if p.SystemPrompt != "你是 x" || p.Config["history_turns"] != 5 {
		t.Fatalf("persona x parsed wrong: %+v", p)
	}
	if d, _ := reg.Get("default"); d.SystemPrompt != "外部默认" {
		t.Fatalf("external default did not override embedded: %q", d.SystemPrompt)
	}
	if p.Scope() != "x" {
		t.Fatalf("persona scope=%q", p.Scope())
	}

	cfg := NewConfig()
	cfg.Set("", "history_turns", 20)
	reg.Apply(cfg)
	if got, _ := cfg.Get("x", "history_turns"); got != 5 {
		t.Fatalf("applied persona config=%v, want 5", got)
	}
	if got, _ := cfg.Get("", "history_turns"); got != 20 {
		t.Fatalf("global changed by Apply: %v", got)
	}

	if err := reg.ValidateRefs("x", "default"); err != nil {
		t.Fatalf("ValidateRefs(known): %v", err)
	}
	if err := reg.ValidateRefs("nope"); err == nil {
		t.Fatal("ValidateRefs(unknown) succeeded")
	}
}

func Test_F82_PersonaValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{"missing field", "m.yml", "name: m\n", "缺少必填字段"},
		{"illegal name", "Bad.yml", "name: Bad\nsystem_prompt: hi\n", "非法人格名"},
		{"name mismatch", "a.yml", "name: b\nsystem_prompt: hi\n", "不一致"},
		{"unknown field", "k.yml", "name: k\nsystem_prompt: hi\nbogus: 1\n", "解析失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePersona(t, dir, tc.file, tc.body)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v does not contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("error %v does not name file %q", err, tc.file)
			}
		})
	}
}

func Test_F82_PersonaDuplicateAndPromptFile(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		dir := t.TempDir()
		writePersona(t, dir, "same.yml", "name: same\nsystem_prompt: a\n")
		writePersona(t, dir, "same.yaml", "name: same\nsystem_prompt: b\n")
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), "都定义人格") {
			t.Fatalf("Load dup err=%v", err)
		}
	})
	t.Run("prompt_file", func(t *testing.T) {
		dir := t.TempDir()
		writePersona(t, dir, "p.md", "角色正文\n")
		writePersona(t, dir, "p.yml", "name: p\nprompt_file: p.md\n")
		reg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		p, ok := reg.Get("p")
		if !ok || strings.TrimSpace(p.SystemPrompt) != "角色正文" {
			t.Fatalf("prompt_file not expanded: %+v", p)
		}
	})
	t.Run("missing prompt_file", func(t *testing.T) {
		dir := t.TempDir()
		writePersona(t, dir, "p.yml", "name: p\nprompt_file: nope.md\n")
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), "读取 prompt_file") {
			t.Fatalf("Load missing prompt err=%v", err)
		}
	})
}

func Test_F82_ReloadKeepsOldOnFailure(t *testing.T) {
	good := t.TempDir()
	writePersona(t, good, "default.yml", "name: default\nsystem_prompt: 旧默认\n")
	reg, err := Load(good)
	if err != nil {
		t.Fatalf("Load good: %v", err)
	}

	bad := t.TempDir()
	writePersona(t, bad, "default.yml", "name: default\nbogus: 1\n")
	var warned []string
	reg.Reload(bad, func(msg string) { warned = append(warned, msg) })
	if len(warned) == 0 {
		t.Fatal("Reload did not warn")
	}
	if !strings.Contains(warned[0], "保留旧版本") {
		t.Fatalf("warn=%q", warned[0])
	}
	if p, _ := reg.Get("default"); p.SystemPrompt != "旧默认" {
		t.Fatalf("registry changed after failed reload: %q", p.SystemPrompt)
	}
}

func Test_F82_PersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePersona(t, dir, "default.yml", "name: default\nsystem_prompt: 默认正文\n")
	writePersona(t, dir, "x.yml", "name: x\nsystem_prompt: x 正文\n")
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg := NewConfig()
	reg.Apply(cfg)
	store := NewMemoryStore()
	var buf bytes.Buffer
	mgr, err := NewManager(cfg, reg, store, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ref := SessionRef{Scope: GroupScope("g1"), User: "u1"}
	if p, err := mgr.Persona(ref); err != nil || p != DefaultPersona {
		t.Fatalf("initial persona=(%q,%v)", p, err)
	}
	changed, err := mgr.SetPersona(ref, "x")
	if err != nil || !changed {
		t.Fatalf("SetPersona x=(%v,%v), want (true,nil)", changed, err)
	}
	if p, _ := mgr.Persona(ref); p != "x" {
		t.Fatalf("persona after switch=%q", p)
	}
	// 同一 store 的新 Manager 必须看到持久化结果（持久化往返）。
	mgr2, err := NewManager(cfg, reg, store, nil)
	if err != nil {
		t.Fatalf("NewManager2: %v", err)
	}
	if p, _ := mgr2.Persona(ref); p != "x" {
		t.Fatalf("persisted persona=%q, want x", p)
	}
	rk1, _ := mgr.RouteKey(ref)
	rk2, _ := mgr2.RouteKey(ref)
	if rk1 != rk2 {
		t.Fatalf("route key not stable across managers: %q vs %q", rk1, rk2)
	}

	// 切换人格必须让 F-65 半静态段哈希变化。
	fpBefore, _ := mgr.Fingerprint(ref)
	changed, err = mgr.SetPersona(ref, "default")
	if err != nil || !changed {
		t.Fatalf("SetPersona default=(%v,%v), want (true,nil)", changed, err)
	}
	fpAfter, _ := mgr.Fingerprint(ref)
	if fpBefore == fpAfter {
		t.Fatal("persona fingerprint did not change after switch")
	}
	if !strings.Contains(buf.String(), "人格已切换") {
		t.Fatalf("no persona-change log: %q", buf.String())
	}

	// 设置同一人格不算变化。
	if changed, err := mgr.SetPersona(ref, "default"); err != nil || changed {
		t.Fatalf("re-set same persona=(%v,%v), want (false,nil)", changed, err)
	}
	// 非法或未定义的人格必须被拒绝。
	if _, err := mgr.SetPersona(ref, "Bad"); err == nil {
		t.Fatal("SetPersona(Bad) succeeded")
	}
	if _, err := mgr.SetPersona(ref, "nope"); err == nil {
		t.Fatal("SetPersona(unknown) succeeded")
	}
}

func Test_F82_DefaultPersonaStrategy(t *testing.T) {
	reg, err := Load("")
	if err != nil {
		t.Fatalf("Load builtin: %v", err)
	}
	if !reg.Has(DefaultPersona) {
		t.Fatal("builtin registry lacks default persona")
	}
	cfg := NewConfig()
	mgr, err := NewManager(cfg, reg, nil, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ref := SessionRef{Scope: GroupScope("g1"), User: "u1"}
	if p, _ := mgr.Persona(ref); p != DefaultPersona {
		t.Fatalf("unconfigured persona=%q, want default", p)
	}
	k1, err := mgr.RouteKey(ref)
	if err != nil {
		t.Fatalf("RouteKey: %v", err)
	}
	k2, _ := mgr.RouteKey(ref)
	if k1 != k2 {
		t.Fatal("route key unstable with default persona")
	}
	// 未注入 store 时拒绝切换，避免静默不落盘。
	if _, err := mgr.SetPersona(ref, DefaultPersona); err == nil {
		t.Fatal("SetPersona without store succeeded")
	}
	// 配置里引用的人格生效。
	cfg.Set(ref.Scope, KeyPersona, DefaultPersona)
	if p, _ := mgr.Persona(ref); p != DefaultPersona {
		t.Fatalf("configured persona=%q", p)
	}
}
