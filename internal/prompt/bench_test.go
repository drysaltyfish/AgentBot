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
	data := map[string]any{
		"BotName":     "AgentBot",
		"Timezone":    "Asia/Shanghai",
		"Now":         time.Unix(0, 0),
		"Persona":     "默认人格",
		"Tools":       []string{"calculator", "current_time"},
		"ToolHeaders": []string{"名称", "说明"},
		"ToolRows":    [][]string{{"calculator", "四则运算"}, {"current_time", "当前时间"}},
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
