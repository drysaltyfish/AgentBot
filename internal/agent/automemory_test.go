package agent

import (
	"context"
	"strings"
	"testing"
)

func Test_F48_ExtractsExplicitMemoryCommands(t *testing.T) {
	t.Parallel()
	c := NewMemoryCommand(nil)
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"记住：我喜欢橘子汁", "我喜欢橘子汁", true},
		{"记住: 我养了只猫", "我养了只猫", true},
		{"  记住 明天要开会  ", "明天要开会", true},
		{"帮我记住：生日是 3 月 5 日", "生日是 3 月 5 日", true},
		{"别忘了：周五交作业", "周五交作业", true},
		// 句中出现"记住"不应触发——否则会产生大量误写。
		{"你还记住我吗", "", false},
		{"我记住了这件事", "", false},
		{"好的，我记住了", "", false},
		// 指令不完整。
		{"记住：", "", false},
		{"记住", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := c.Extract(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("Extract(%q) = (%q, %v)，期望 (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// Test_F48_RejectsIllegalFacts 保证非法内容不会被写进记忆。
func Test_F48_RejectsIllegalFacts(t *testing.T) {
	t.Parallel()
	c := NewMemoryCommand(nil)
	if _, ok := c.Extract("记住：" + strings.Repeat("字", MemoryLimit+1)); ok {
		t.Fatalf("超长内容不应被当作记忆")
	}
}

func Test_F48_CustomTriggers(t *testing.T) {
	t.Parallel()
	c := NewMemoryCommand([]string{"帮我存："})
	if got, ok := c.Extract("帮我存：钥匙在抽屉里"); !ok || got != "钥匙在抽屉里" {
		t.Fatalf("自定义触发词未生效: %q %v", got, ok)
	}
	// 默认触发词在自定义后被替换（不是叠加）。
	if _, ok := c.Extract("记住：x"); ok {
		t.Fatalf("自定义触发词应替换默认列表")
	}
	if len(c.Triggers()) != 1 {
		t.Fatalf("Triggers 应返回生效列表: %v", c.Triggers())
	}
}

// Test_F48_AutoMemoryWritesToTheScopedStore 端到端：指令 -> 写入 -> 注入下一轮。
func Test_F48_AutoMemoryWritesToTheScopedStore(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	scope := "group-111"

	fact, ok := NewMemoryCommand(nil).Extract("记住：我讨厌香菜")
	if !ok {
		t.Fatalf("应识别出记忆指令")
	}
	if err := mem.Save(WithMemoryScope(context.Background(), scope), fact); err != nil {
		t.Fatalf("自动写入失败: %v", err)
	}

	// 只在本作用域可见。
	got, _ := mem.Recall(WithMemoryScope(context.Background(), scope))
	if len(got) != 1 || got[0] != "我讨厌香菜" {
		t.Fatalf("本作用域应读到: %v", got)
	}
	other, _ := mem.Recall(WithMemoryScope(context.Background(), "group-222"))
	if len(other) != 0 {
		t.Fatalf("自动写入也必须按作用域隔离: %v", other)
	}
}
