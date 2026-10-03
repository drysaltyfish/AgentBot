package agent

import (
	"fmt"
	"strings"
)

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

// DefaultToolUsageInstruction 提示模型如何用工具回溯历史。
//
// 为什么需要它：被引用的消息现在会进入本轮输入，但**只有那一句**。
// 如果那句话很久远，它周围发生了什么并不在上下文里——而 recall_history
// 恰好能按关键词检索并返回前后文。模型不知道有这个手段，就不会用。
const DefaultToolUsageInstruction = "关于回溯：当对方引用了很久以前的话、" +
	"而被引用的内容缺少上下文时，你可以用 recall_history 检索那句话里的关键词，" +
	"它会返回命中处以及前后各一条，帮你还原当时在聊什么。"

// ToolUsageInstruction 返回生效的工具使用提示。
func ToolUsageInstruction(custom string) string {
	if s := strings.TrimSpace(custom); s != "" {
		return s
	}
	return DefaultToolUsageInstruction
}

// SelfIdentity 生成"你是谁"的说明，让模型能认出 @ 自己。
//
// 缺了它，模型看到 "@10001" 只会当成一串陌生数字——
// 表现为"它不知道别人在叫它"，也不会意识到那是自己的话。
func SelfIdentity(selfID int64, nickname string) string {
	if selfID <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "你的 QQ 号是 %d。", selfID)
	if n := strings.TrimSpace(nickname); n != "" {
		fmt.Fprintf(&b, "你在群里的昵称是「%s」。", n)
	}
	fmt.Fprintf(&b, "消息里出现 @%d、或有人引用你的话，都是在跟你说话；", selfID)
	fmt.Fprintf(&b, "看到 %d 这个号说话，那是你自己刚才说的。", selfID)
	return b.String()
}
