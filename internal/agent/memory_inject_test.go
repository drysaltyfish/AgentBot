package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// directFakeMemory 是只读的 Memory stub：验证"直连路径也注入记忆"。
type directFakeMemory struct{ items []string }

func (f directFakeMemory) Save(context.Context, string) error { return nil }

func (f directFakeMemory) Recall(context.Context) ([]string, error) { return f.items, nil }

// Test_F49_DirectAgentInjectsMemory 钉住接线：关掉 ReAct 不等于关掉记忆。
// 记忆必须按 ADR-0002 注入在 system 之后、历史之前。
func Test_F49_DirectAgentInjectsMemory(t *testing.T) {
	t.Parallel()
	fake := llm.NewFakeLLM(llm.ScriptedResponse{Response: &llm.ChatResponse{Content: "好的", FinishReason: "stop"}})
	a := &DirectAgent{
		LLM:       fake,
		Assembler: testAssembler("系统提示词"),
		Memory:    directFakeMemory{items: []string{"用户喜欢橙汁"}},
	}
	if _, err := a.Run(context.Background(), Input{Query: "在吗"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := fake.LastRequest()
	if req == nil {
		t.Fatal("没有发出请求")
	}
	if len(req.Messages) != 3 {
		t.Fatalf("消息数=%d（应为 system+记忆+用户）: %+v", len(req.Messages), req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, "橙汁") {
		t.Fatalf("记忆未被注入: %+v", req.Messages)
	}
}
