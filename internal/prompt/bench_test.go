package prompt

import (
	"testing"
	"time"
)

// BenchmarkRenderPrompt 覆盖 F-77 的提示词渲染基准。
//
// 数据用内置 system 模板实际需要的字段，避免基准因为缺 key 直接失败（那不是性能问题）。
func BenchmarkRenderPrompt(b *testing.B) {
	e := New(Options{})
	names := e.Names()
	if len(names) == 0 {
		b.Skip("没有内置模板")
	}
	name := names[0]
	// 内置 system 模板在 F-33 接线后渲染静态前缀：这里给相同的字段形状，
	// 让基准测的是渲染开销，而不是"缺 key 的报错路径"。
	data := map[string]any{
		"SystemPrompt":    "基础提示词",
		"ProactiveMemory": "记忆指令",
		"Identity":        "身份说明",
		"ToolHint":        "工具提示",
		"Timezone":        "Asia/Shanghai",
		"Now":             time.Unix(0, 0),
	}
	if _, err := e.Render(name, data); err != nil {
		b.Fatalf("Render 预热失败: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Render(name, data); err != nil {
			b.Fatalf("Render: %v", err)
		}
	}
}
