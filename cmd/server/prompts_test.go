package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
)

// Test_F33_RenderedPrefixIsByteStable 是 F-33 与 F-65 的交叉验收：
// 同一份输入渲染两次必须逐字节相同，且段落顺序稳定——否则前缀缓存每轮都失效。
func Test_F33_RenderedPrefixIsByteStable(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	eng, err := buildPromptEngine(cfg, testLogger(t))
	if err != nil {
		t.Fatalf("buildPromptEngine: %v", err)
	}
	data := prefixData{
		SystemPrompt:    "基础提示词",
		ProactiveMemory: agent.ProactiveMemoryInstruction(""),
		Identity:        agent.SelfIdentity(10001, ""),
		ToolHint:        agent.ToolUsageInstruction(""),
	}
	first, err := renderSystemPrefix(eng, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := renderSystemPrefix(eng, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if first != second {
		t.Fatalf("两次渲染必须逐字节相同:\n%q\n%q", first, second)
	}
	// 顺序即契约：基础 -> 记忆指令 -> 身份 -> 工具提示。
	idx := func(s string) int { return strings.Index(first, s) }
	if !(idx("基础提示词") < idx("关于记忆") || idx("关于记忆") == -1) {
		t.Fatalf("基础提示词应在最前: %q", first)
	}
	if strings.Index(first, "你的 QQ 号是") < idx("基础提示词") {
		t.Fatalf("身份说明应在基础提示词之后: %q", first)
	}
	if !strings.Contains(first, "关于回溯") {
		t.Fatalf("工具提示应被渲染: %q", first)
	}
	// 未提供的段落不得留下空行或占位符。
	if strings.Contains(first, "{{") || strings.Contains(first, "}}") {
		t.Fatalf("渲染结果不应残留模板语法: %q", first)
	}
}

// Test_F33_ExternalTemplateOverridesBuiltin 覆盖"外部同名文件覆盖内置版本"。
func Test_F33_ExternalTemplateOverridesBuiltin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system.tmpl"),
		[]byte("外部模板：{{ .SystemPrompt }}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := config.Default()
	cfg.Prompt.Dir = dir
	eng, err := buildPromptEngine(cfg, testLogger(t))
	if err != nil {
		t.Fatalf("buildPromptEngine: %v", err)
	}
	out, err := renderSystemPrefix(eng, prefixData{SystemPrompt: "正文"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "外部模板：正文" {
		t.Fatalf("外部模板应覆盖内置版本: %q", out)
	}
}

// Test_F33_CRLFAndLFOverrideRenderIdentically 是 F-33 的硬性验收：
// CRLF 与 LF 两种检出的模板必须渲染出逐字节相同的结果。
func Test_F33_CRLFAndLFOverrideRenderIdentically(t *testing.T) {
	t.Parallel()
	render := func(content string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "system.tmpl"), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		cfg := config.Default()
		cfg.Prompt.Dir = dir
		eng, err := buildPromptEngine(cfg, testLogger(t))
		if err != nil {
			t.Fatalf("buildPromptEngine: %v", err)
		}
		out, err := renderSystemPrefix(eng, prefixData{SystemPrompt: "A", Identity: "B"})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return out
	}
	lf := render("{{ .SystemPrompt }}\n{{ .Identity }}\n")
	crlf := render("{{ .SystemPrompt }}\r\n{{ .Identity }}\r\n")
	if lf != crlf {
		t.Fatalf("CRLF 与 LF 渲染结果必须逐字节相同:\nLF=%q\nCRLF=%q", lf, crlf)
	}
}

// Test_F33_TemplateErrorsFailAtStartup 钉住"启动期检出"：
// 写错变量名或语法错误必须在服务起来之前失败，而不是等某条分支被走到。
func Test_F33_TemplateErrorsFailAtStartup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
	}{
		{"unknown field", "{{ .NoSuchField }}\n"},
		{"bad syntax", "{{ if .SystemPrompt }}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "system.tmpl"), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			cfg := config.Default()
			cfg.Prompt.Dir = dir
			if _, err := buildPromptEngine(cfg, testLogger(t)); err == nil {
				t.Fatal("模板错误必须让启动失败")
			}
		})
	}
}
