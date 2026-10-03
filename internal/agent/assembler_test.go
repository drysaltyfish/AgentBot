package agent

import "github.com/drysaltyfish/agentbot/internal/conversation"

// testAssembler 构造测试用装配器。
//
// 布局规则只有一份（internal/conversation），因此测试也走真实的装配路径：
// 前缀固定、呈现窗口与环境消息压缩都按生产同样的规则执行。
func testAssembler(system string) *conversation.Assembler {
	return conversation.New(conversation.Options{System: system})
}
