package agent

import "strings"

// DefaultProactiveMemoryInstruction 是要求模型主动保存长期记忆的系统提示词片段。
//
// 与規則觸發（用户明说"记住"）不同，这条通道让模型**自己判断**什么值得长期记住。
// 它是概率性的：可能漏记，也可能误记，所以指令里明确划了"该记"与"不该记"的边界，
// 并要求一次只记一条——把噪声挡在存储之外，比事后清理便宜得多。
const DefaultProactiveMemoryInstruction = "关于长期记忆：当对方透露值得长期记住的事实时" +
	"（称呼与姓名、稳定的喜好与厌恶、习惯、重要日期、承诺、正在长期经历的事情），" +
	"请主动调用 save_memory 记下来。一次只记一条，用一句话陈述事实、不要加修饰。" +
	"寒暄、玩笑、临时状态、以及你自己说的话都不要记。" +
	"已经记过的内容不要重复记；不确定是否值得记时，宁可记下。"

// ProactiveMemoryInstruction 返回生效的指令文本。
func ProactiveMemoryInstruction(custom string) string {
	if s := strings.TrimSpace(custom); s != "" {
		return s
	}
	return DefaultProactiveMemoryInstruction
}

// ComposeSystemPrompt 把长期记忆指令并入系统提示词。
//
// 追加在**末尾**：人格部分保持不变，指令与人格在视觉上分开；
// 同时整段提示词仍是**不可变前缀**（只随配置变化），不影响前缀缓存。
func ComposeSystemPrompt(base, instruction string) string {
	base = strings.TrimRight(base, " \t\r\n")
	instr := strings.TrimSpace(instruction)
	if instr == "" {
		return base
	}
	return base + "\n\n" + instr
}
