package agent

import (
	"strings"
	"testing"
)

func Test_ProactiveMemory_ComposesInstructionAtTheEnd(t *testing.T) {
	t.Parallel()
	base := "你是香橙娘。"
	got := ComposeSystemPrompt(base, "记住重要事实。")

	if !strings.HasPrefix(got, base) {
		t.Fatalf("人格部分必须原样保持开头（它是不可变前缀的起点）: %q", got)
	}
	if !strings.HasSuffix(got, "记住重要事实。") {
		t.Fatalf("指令应追加在末尾: %q", got)
	}
	if strings.Count(got, "记住重要事实。") != 1 {
		t.Fatalf("指令只应出现一次: %q", got)
	}
}

func Test_ProactiveMemory_StabilityMatters(t *testing.T) {
	t.Parallel()
	// 同样的输入必须得到逐字节相同的结果——否则前缀缓存每轮都会失效。
	base := "你是香橙娘。\n"
	a := ComposeSystemPrompt(base, DefaultProactiveMemoryInstruction)
	b := ComposeSystemPrompt(base, DefaultProactiveMemoryInstruction)
	if a != b {
		t.Fatalf("同样的输入必须产生相同的前缀")
	}
	// 末尾空白应被规整，避免"看起来一样但字节不同"。
	if strings.Contains(a, "\n\n\n") {
		t.Fatalf("不应留下多余空行: %q", a)
	}
}

func Test_ProactiveMemory_EmptyInstructionKeepsBase(t *testing.T) {
	t.Parallel()
	base := "你是香橙娘。"
	if got := ComposeSystemPrompt(base, "   "); got != base {
		t.Fatalf("空指令不应改动提示词: %q", got)
	}
	if got := ComposeSystemPrompt(base, ""); got != base {
		t.Fatalf("空指令不应改动提示词: %q", got)
	}
}

func Test_ProactiveMemory_CustomOverridesDefault(t *testing.T) {
	t.Parallel()
	if got := ProactiveMemoryInstruction("  自定义指令  "); got != "自定义指令" {
		t.Fatalf("自定义指令应被 trim 后返回: %q", got)
	}
	if got := ProactiveMemoryInstruction(""); got != DefaultProactiveMemoryInstruction {
		t.Fatalf("空时应回退到内置指令")
	}
	// 内置指令必须真的表达了"该记什么/不该记什么"以及"一次一条"。
	low := DefaultProactiveMemoryInstruction
	for _, want := range []string{"save_memory", "一次只记一条", "不要记", "不要重复记"} {
		if !strings.Contains(low, want) {
			t.Fatalf("内置指令应包含 %q: %q", want, low)
		}
	}
}
