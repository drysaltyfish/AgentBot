package testutil_test

import (
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/prompt"
	"github.com/drysaltyfish/agentbot/internal/testutil"
	"github.com/drysaltyfish/agentbot/internal/tool"
	"github.com/drysaltyfish/agentbot/internal/tool/builtin"
)

// goldenSample 与 internal/prompt 的单测保持一致：固定时钟 + 固定数据，
// 让模板渲染逐字节确定（F-74 要求固定 clock 注入，而不是跳过某一行）。
func goldenSample() map[string]any {
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

// Test_F74_GoldenPromptRendering 对每个内置模板做逐字节黄金比对。
func Test_F74_GoldenPromptRendering(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	engine := prompt.New(prompt.Options{
		SampleData: goldenSample(),
		Now:        func() time.Time { return now },
	})
	names := engine.Names()
	if len(names) == 0 {
		t.Fatal("没有可渲染的内置模板")
	}
	for _, name := range names {
		out, err := engine.Render(name, goldenSample())
		if err != nil {
			t.Fatalf("Render(%s): %v", name, err)
		}
		testutil.AssertString(t, "prompt/"+name, out)
	}
}

// Test_F74_GoldenPolicyRender 对每个角色渲染一份权限表并做黄金比对。
func Test_F74_GoldenPolicyRender(t *testing.T) {
	t.Parallel()
	p, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	roles := p.Roles()
	if len(roles) == 0 {
		t.Fatal("默认权限表没有任何角色")
	}
	for _, role := range roles {
		out, err := p.Render(role)
		if err != nil {
			t.Fatalf("Render(%s): %v", role, err)
		}
		testutil.AssertString(t, "policy/"+role, out)
	}
}

// Test_F74_GoldenToolSchemas 对内置工具导出的 function schema 列表做黄金比对。
//
// 顺序与内容一旦改变，模型看到的工具清单就变了（前缀缓存与行为都会受影响），
// 因此这里用黄金文件把它钉死。时间源固定，避免 current_time 之类工具带来抖动。
func Test_F74_GoldenToolSchemas(t *testing.T) {
	t.Parallel()
	registry := tool.New()
	if err := builtin.Register(registry, builtin.Deps{
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var b strings.Builder
	for _, spec := range registry.Definitions() {
		b.WriteString(spec.Name)
		b.WriteString("\n  desc: ")
		b.WriteString(spec.Description)
		b.WriteString("\n  params: ")
		b.Write(spec.Parameters)
		b.WriteString("\n")
	}
	testutil.AssertString(t, "tool/schemas", b.String())
}
