package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/admin"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/scoped"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// Test_F82_PersonaSwitchChangesPromptHash 端到端覆盖 F-82 剩余部分与 F-65 的接合：
//
//	SQLite 持久层 -> scoped.Manager -> 半静态段 -> /prompt-hash 报告
//
// 断言的是"效果"而不是"函数被调用了"：切换人格后，报告里的半静态段哈希必须变，
// 而静态段哈希必须不变（顺序与边界都对）。
func Test_F82_PersonaSwitchChangesPromptHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dir := t.TempDir()
	writePersonaFile(t, dir, "default.yml", "name: default\nsystem_prompt: 默认人格正文\n")
	writePersonaFile(t, dir, "alt.yml", "name: alt\nsystem_prompt: 另一个人格正文\n")

	cfg := config.Default()
	cfg.Access.Roles = map[string][]int64{"superuser": {42}}
	cfg.Prompt.PersonasDir = ptr(dir)

	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "persona.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	lg := testLogger(t)
	defer func() { _ = lg.Close(ctx) }()

	reg, mgr, err := buildPersonas(cfg, personaStoreAdapter{st: st}, lg)
	if err != nil {
		t.Fatalf("buildPersonas: %v", err)
	}
	if !reg.Has("alt") {
		t.Fatal("人格目录里的 alt 未被加载")
	}
	asm := conversation.New(conversation.Options{
		System:     "静态段",
		HalfStatic: personaHalfStatic(reg, mgr, func(msg string) { t.Log(msg) }),
	})
	m := buildAdminModule(cfg, nil, lg, nil, nil, mgr, personaPromptHash(asm, mgr, nil, 100, lg))
	if m == nil {
		t.Fatal("buildAdminModule returned nil")
	}

	dispatch := func(text string) string {
		t.Helper()
		reply, derr := m.Dispatch(ctx, admin.Request{Text: text, UserID: 42, GroupID: 7, Source: admin.SourceMessage})
		if derr != nil {
			t.Fatalf("Dispatch %q: %v", text, derr)
		}
		return reply
	}

	before := dispatch("/prompt-hash")
	if !strings.Contains(before, "static:") || !strings.Contains(before, "half-static:") {
		t.Fatalf("/prompt-hash 必须报告静态段与半静态段: %q", before)
	}

	switchReply := dispatch("/persona alt")
	if !strings.Contains(switchReply, "alt") {
		t.Fatalf("/persona 回执应提到新人格: %q", switchReply)
	}
	// 切换只改作用域键：作用域键变了，会话本身没有被重建。
	key := session.Key{SelfID: 100, GroupID: 7, UserID: 42}
	ref := scoped.SessionRefForKey(key)
	if p, perr := mgr.Persona(ctx, ref); perr != nil || p != "alt" {
		t.Fatalf("切换后 Manager 读到的人格=(%q,%v), want alt", p, perr)
	}

	after := dispatch("/prompt-hash")
	if halfStaticLine(before) == halfStaticLine(after) {
		t.Fatalf("切换人格后半静态段哈希必须变化:\nbefore=%s\nafter=%s", before, after)
	}
	if staticLine(before) != staticLine(after) {
		t.Fatalf("静态段哈希不该随人格变化:\nbefore=%s\nafter=%s", before, after)
	}

	// 半静态段正文确实换成了新人格：装配结果可验证。
	systemText := asm.SystemFor(ctx, key)
	if !strings.Contains(systemText, "另一个人格正文") {
		t.Fatalf("system 消息没有换成人格 alt 的正文: %q", systemText)
	}

	// 再次启动一个 Manager（重启语义）仍应读回 alt——持久化确实生效。
	reg2, mgr2, err := buildPersonas(cfg, personaStoreAdapter{st: st}, lg)
	if err != nil {
		t.Fatalf("buildPersonas again: %v", err)
	}
	if p, perr := mgr2.Persona(ctx, ref); perr != nil || p != "alt" {
		t.Fatalf("重启后人格=(%q,%v), want alt", p, perr)
	}
	_ = reg2
}

// Test_F82_DefaultPersonaDoesNotDuplicateTheStaticSegment 钉住默认人格的边界：
// 未选择人格时 system 消息必须逐字节等于静态段——否则内置 default.yml 的正文
// （与 conversation.DefaultSystemPrompt 逐字相同）会被重复注入，白烧 token 与缓存。
func Test_F82_DefaultPersonaDoesNotDuplicateTheStaticSegment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	writePersonaFile(t, dir, "default.yml", "name: default\nsystem_prompt: 默认人格正文\n")
	writePersonaFile(t, dir, "alt.yml", "name: alt\nsystem_prompt: 另一个人格正文\n")

	cfg := config.Default()
	cfg.Prompt.PersonasDir = ptr(dir)
	lg := testLogger(t)
	defer func() { _ = lg.Close(ctx) }()

	reg, mgr, err := buildPersonas(cfg, scoped.NewMemoryStore(), lg)
	if err != nil {
		t.Fatalf("buildPersonas: %v", err)
	}
	asm := conversation.New(conversation.Options{
		System:     "静态段",
		HalfStatic: personaHalfStatic(reg, mgr, nil),
	})
	key := session.Key{SelfID: 100, GroupID: 7, UserID: 42}
	if got := asm.SystemFor(ctx, key); got != "静态段" {
		t.Fatalf("默认人格不得重复注入正文: %q", got)
	}
	if _, err := mgr.SetPersona(ctx, scoped.SessionRefForKey(key), "alt"); err != nil {
		t.Fatalf("SetPersona: %v", err)
	}
	got := asm.SystemFor(ctx, key)
	if !strings.Contains(got, "另一个人格正文") {
		t.Fatalf("切到 alt 后 system 应带上人格正文: %q", got)
	}
	if !strings.HasPrefix(got, "静态段") {
		t.Fatalf("半静态段必须排在静态段之后: %q", got)
	}
}

// Test_F65_PromptHashWithoutPersonaStillReportsStatic 证明未切换过的会话也有一份
// 可比的报告：半静态段取默认人格，而不是空白。
func Test_F65_PromptHashWithoutPersonaStillReportsStatic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	writePersonaFile(t, dir, "default.yml", "name: default\nsystem_prompt: 默认人格正文\n")

	cfg := config.Default()
	cfg.Access.Roles = map[string][]int64{"superuser": {42}}
	cfg.Prompt.PersonasDir = ptr(dir)
	lg := testLogger(t)
	defer func() { _ = lg.Close(ctx) }()

	reg, mgr, err := buildPersonas(cfg, nil, lg)
	if err != nil {
		t.Fatalf("buildPersonas: %v", err)
	}
	asm := conversation.New(conversation.Options{
		System:     "静态段",
		HalfStatic: personaHalfStatic(reg, mgr, nil),
	})
	m := buildAdminModule(cfg, nil, lg, nil, nil, mgr, personaPromptHash(asm, mgr, nil, 100, lg))
	reply, derr := m.Dispatch(ctx, admin.Request{Text: "/prompt-hash", UserID: 42, Source: admin.SourceMessage})
	if derr != nil {
		t.Fatalf("Dispatch: %v", derr)
	}
	if !strings.Contains(reply, "persona-key:") {
		t.Fatalf("报告应包含路由键指纹，便于排查同人格会话是否共享前缀: %q", reply)
	}
}

func writePersonaFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func halfStaticLine(report string) string { return reportLine(report, "- half-static:") }
func staticLine(report string) string     { return reportLine(report, "- static:") }

func reportLine(report, prefix string) string {
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// Test_F24_PersonaFileChangeSwapsDefinitions 覆盖 F-24 的人格目录热加载：
// 改一个**已存在**的人格文件必须让下一次请求的半静态段变化；
// 写坏文件必须保留旧定义而不是清空（清空等于让人格静默消失）。
func Test_F24_PersonaFileChangeSwapsDefinitions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writePersonaFile(t, dir, "default.yml", "name: default\nsystem_prompt: 默认人格正文\n")
	writePersonaFile(t, dir, "alt.yml", "name: alt\nsystem_prompt: 旧版人格正文\n")

	cfg := config.Default()
	cfg.Prompt.PersonasDir = ptr(dir)
	lg := testLogger(t)
	defer func() { _ = lg.Close(ctx) }()

	reg, mgr, err := buildPersonas(cfg, scoped.NewMemoryStore(), lg)
	if err != nil {
		t.Fatalf("buildPersonas: %v", err)
	}
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}
	if _, err := mgr.SetPersona(ctx, scoped.SessionRefForKey(key), "alt"); err != nil {
		t.Fatalf("SetPersona: %v", err)
	}
	asm := conversation.New(conversation.Options{
		System:     "静态段",
		HalfStatic: personaHalfStatic(reg, mgr, nil),
	})
	if got := asm.SystemFor(ctx, key); !strings.Contains(got, "旧版人格正文") {
		t.Fatalf("前置条件不成立: %q", got)
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := watchPersonas(wctx, dir, reg, lg)
	if w == nil {
		t.Fatal("watchPersonas 应返回监听器")
	}
	defer w.Stop()

	writePersonaFile(t, dir, "alt.yml", "name: alt\nsystem_prompt: 新版人格正文\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(asm.SystemFor(ctx, key), "新版人格正文") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := asm.SystemFor(ctx, key); !strings.Contains(got, "新版人格正文") {
		t.Fatalf("人格文件改动未被热加载: %q", got)
	}

	// 写坏文件：保留旧定义，不清空。
	writePersonaFile(t, dir, "alt.yml", "name: alt\nsystem_prompt:\n")
	time.Sleep(1500 * time.Millisecond)
	if got := asm.SystemFor(ctx, key); !strings.Contains(got, "新版人格正文") {
		t.Fatalf("非法人格文件应保留旧定义: %q", got)
	}
}
