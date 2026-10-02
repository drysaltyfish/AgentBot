package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() map[string]any {
	return map[string]any{
		"BotName":     "AgentBot",
		"Timezone":    "Asia/Shanghai",
		"Now":         time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		"Persona":     "default",
		"Tools":       true,
		"ToolHeaders": []string{"功能", "action"},
		"ToolRows":    [][]string{{"查询天气", "get_weather"}},
	}
}

func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".tmpl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

func Test_F33_NormalizeUnifiesLineEndings(t *testing.T) {
	t.Parallel()
	got := Normalize("a\r\nb\rc\n")
	want := "a\nb\nc\n"
	if got != want {
		t.Fatalf("Normalize: actual=%q expected=%q", got, want)
	}
}

func Test_F33_CrlfAndLfRendersAreByteIdentical(t *testing.T) {
	t.Parallel()
	body := "A\nB {{.BotName}}\nC\n"
	lfDir := t.TempDir()
	crlfDir := t.TempDir()
	writeTemplate(t, lfDir, "demo", body)
	writeTemplate(t, crlfDir, "demo", strings.ReplaceAll(body, "\n", "\r\n"))

	lfOut, err := New(Options{Dir: lfDir, SampleData: sample()}).Render("demo", sample())
	if err != nil {
		t.Fatalf("render LF: %v", err)
	}
	crlfOut, err := New(Options{Dir: crlfDir, SampleData: sample()}).Render("demo", sample())
	if err != nil {
		t.Fatalf("render CRLF: %v", err)
	}
	if lfOut != crlfOut {
		t.Fatalf("CRLF and LF renders differ: lf=%q crlf=%q", lfOut, crlfOut)
	}
	if strings.Contains(crlfOut, "\r") {
		t.Fatalf("rendered output still contains CR: %q", crlfOut)
	}
}

func Test_F33_MissingVariableIsReportedWithLineNumber(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "broken", "line1\nline2 {{.Nope}}\n")
	err := New(Options{Dir: dir, SampleData: sample()}).ValidateStartup()
	if err == nil {
		t.Fatalf("ValidateStartup: actual=nil expected=error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Nope") {
		t.Fatalf("error should name the missing variable: %v", msg)
	}
	if !strings.Contains(msg, ":2:") {
		t.Fatalf("error should carry the line number (want :2:): %v", msg)
	}
}

func Test_F33_SyntaxErrorIsReported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "syntax", "ok\n{{ if .Tools }}\nunclosed\n")
	err := New(Options{Dir: dir, SampleData: sample()}).ValidateStartup()
	if err == nil {
		t.Fatalf("ValidateStartup: actual=nil expected=error")
	}
	if !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("error should name the template: %v", err)
	}
}

func Test_F33_BuiltinTemplateIsValidatedAtStartup(t *testing.T) {
	t.Parallel()
	e := New(Options{SampleData: sample()})
	if err := e.ValidateStartup(); err != nil {
		t.Fatalf("builtin templates failed validation: %v", err)
	}
	out, err := e.Render("system", sample())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "AgentBot") {
		t.Fatalf("rendered system prompt missing bot name: %q", out)
	}
	if !strings.Contains(out, "| 功能 | action |") {
		t.Fatalf("mdTable output missing header: %q", out)
	}
	if !strings.Contains(out, "2026-10-02T20:00:00+08:00") {
		t.Fatalf("timezone formatting wrong: %q", out)
	}
}

func Test_F33_ExternalOverridesBuiltin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "system", "OVERRIDDEN {{.BotName}}")
	out, err := New(Options{Dir: dir, SampleData: sample()}).Render("system", sample())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(out, "OVERRIDDEN") {
		t.Fatalf("external template did not override builtin: %q", out)
	}
	if _, err := New(Options{SampleData: sample()}).Render("does-not-exist", sample()); err == nil {
		t.Fatalf("missing template: actual=nil expected=error")
	}
}

func Test_F33_RenderIsCachedAndHashIsStable(t *testing.T) {
	t.Parallel()
	e := New(Options{SampleData: sample()})
	first, hash1, err := e.RenderWithHash("system", sample())
	if err != nil {
		t.Fatalf("RenderWithHash: %v", err)
	}
	second, hash2, err := e.RenderWithHash("system", sample())
	if err != nil {
		t.Fatalf("RenderWithHash: %v", err)
	}
	if first != second || hash1 != hash2 {
		t.Fatalf("render is not deterministic/cached: hashes %q vs %q", hash1, hash2)
	}
	data := sample()
	data["BotName"] = "Other"
	_, hash3, err := e.RenderWithHash("system", data)
	if err != nil {
		t.Fatalf("RenderWithHash: %v", err)
	}
	if hash3 == hash1 {
		t.Fatalf("hash did not change when data changed")
	}
}

func Test_F33_SlowRenderReportsWithoutFailing(t *testing.T) {
	t.Parallel()
	var got string
	e := New(Options{
		SampleData: sample(),
		Timeout:    time.Nanosecond,
		// 注入确定性的耗时，避免依赖真实时钟粒度（Windows 上会偶发为 0）。
		Measure:      func(time.Time) time.Duration { return time.Hour },
		OnSlowRender: func(name string, took time.Duration) { got = name },
	})
	if _, err := e.Render("system", sample()); err != nil {
		t.Fatalf("slow render must not fail: %v", err)
	}
	if got != "system" {
		t.Fatalf("OnSlowRender not called: actual=%q", got)
	}
}

func Test_F33_NamesIncludesBuiltinAndExternal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "custom", "x")
	names := strings.Join(New(Options{Dir: dir, SampleData: sample()}).Names(), ",")
	if !strings.Contains(names, "system") || !strings.Contains(names, "custom") {
		t.Fatalf("Names(): actual=%q expected to contain system and custom", names)
	}
}
