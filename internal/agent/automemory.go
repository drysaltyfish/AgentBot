package agent

import (
	"sort"
	"strings"
)

// DefaultMemoryTriggers 是识别"显式记忆指令"的默认前缀。
//
// F-48 规定写入时机为「模型显式调用 save_memory，或规则触发（如"记住：xxx"命令）」。
// 这里实现的就是后者：**确定性**、不额外调用 LLM、且每条写入都可审计。
//
// 刻意不做"让模型自己判断什么重要"：那是 F-49 的固化（P2/M4），两者不是一回事——
// 规则触发是用户明确说"记住"时的可靠通道，模型判断是概率性的补充。
var DefaultMemoryTriggers = []string{
	"帮我记住：", "帮我记住:", "帮我记住 ",
	"记住：", "记住:", "记住 ", "记住,",
	"记一下：", "记一下:", "记一下 ",
	"别忘了：", "别忘了:", "别忘了 ",
}

// MemoryCommand 把用户消息里的显式记忆指令抽成一条待保存的事实。
//
// 零值不可用，请用 NewMemoryCommand 构造。
type MemoryCommand struct {
	// triggers 按长度降序排列，避免短前缀抢先匹配。
	triggers []string
}

// NewMemoryCommand 构造；triggers 为空时使用 DefaultMemoryTriggers。
func NewMemoryCommand(triggers []string) *MemoryCommand {
	list := triggers
	if len(list) == 0 {
		list = DefaultMemoryTriggers
	}
	cp := make([]string, 0, len(list))
	for _, t := range list {
		if s := strings.TrimSpace(t); s != "" {
			cp = append(cp, s)
		}
	}
	sort.SliceStable(cp, func(i, j int) bool { return len(cp[i]) > len(cp[j]) })
	return &MemoryCommand{triggers: cp}
}

// Extract 判断这条消息是不是"记住：xxx"指令，并抽出 xxx。
//
// 只认**行首**触发词：句中出现"记住"（例如"你还记住我吗"）不该被当成写入指令，
// 否则会产生大量误写——误写的记忆会污染后续每一次注入。
func (c *MemoryCommand) Extract(text string) (string, bool) {
	if c == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	for _, trigger := range c.triggers {
		if !strings.HasPrefix(trimmed, trigger) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, trigger))
		if rest == "" {
			// "记住：" 后面没内容：这是指令不完整，不是一条记忆。
			return "", false
		}
		fact, err := validateMemoryText(rest)
		if err != nil {
			// 太长/多行等非法内容交给上层记日志，不静默吞掉。
			return "", false
		}
		return fact, true
	}
	return "", false
}

// Triggers 返回生效的触发词（副本）。
func (c *MemoryCommand) Triggers() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.triggers))
	copy(out, c.triggers)
	return out
}
