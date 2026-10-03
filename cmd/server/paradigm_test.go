package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

// scriptLLM 按脚本返回内容，并记录调用次数（确定性，无网络）。
type scriptLLM struct {
	mu        sync.Mutex
	responses []string
	calls     int
}

func (s *scriptLLM) Chat(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.responses) == 0 {
		return &llm.ChatResponse{Content: "1.0 兜底"}, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return &llm.ChatResponse{Content: r}, nil
}

func (s *scriptLLM) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.Chunk, error) {
	return nil, nil
}

func (s *scriptLLM) callsMade() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// echoAgent 是最小的 Agent 实现，避免测试依赖 Assembler 等无关装配。
type echoAgent struct{ text string }

func (e echoAgent) Run(context.Context, agent.Input) (*agent.Output, error) {
	return &agent.Output{Text: e.text}, nil
}

// Test_F36_ReflexionParadigmDrivesEvaluation 覆盖 F-36 的接线：
// 配了 paradigm=reflexion 之后，评估器必须真的被调用并驱动重试——
// 否则就是"接了但空转"（Evaluator 为 nil 时 Reflexion 只返回初稿）。
func Test_F36_ReflexionParadigmDrivesEvaluation(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Agent.Paradigm = ptr("reflexion")
	cfg.Agent.Reflexion.MaxReflections = ptr(1)

	// 第一次评估 0.2（不够好）-> 触发反思与重试；第二次 1.0（达标）-> 停止。
	stub := &scriptLLM{responses: []string{"0.2 太短了", "1.0 可以了"}}
	got := wrapParadigm(cfg, stub, echoAgent{text: "初稿"}, testLogger(t))
	if _, ok := got.(*agent.ReflexionAgent); !ok {
		t.Fatalf("paradigm=reflexion 应包成 ReflexionAgent，实际 %T", got)
	}

	out, err := got.Run(context.Background(), agent.Input{Query: "介绍一下你自己"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out == nil || out.Text == "" {
		t.Fatalf("应产出文本")
	}
	if n := stub.callsMade(); n != 2 {
		t.Fatalf("评估器应被调用 2 次（0.2 触发重试、1.0 停止），实际 %d 次", n)
	}
}

// Test_F36_DefaultParadigmKeepsBaseAgent 覆盖边界：未配置时原样返回基础实现。
func Test_F36_DefaultParadigmKeepsBaseAgent(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	base := echoAgent{text: "x"}
	if got := wrapParadigm(cfg, &scriptLLM{}, base, testLogger(t)); got != agent.Agent(base) {
		t.Fatalf("未配置范式时应原样返回基础实现，实际 %T", got)
	}
	// orchestrator 走同样的包装路径。
	cfg.Agent.Paradigm = ptr("orchestrator")
	if _, ok := wrapParadigm(cfg, &scriptLLM{}, base, testLogger(t)).(*agent.Orchestrator); !ok {
		t.Fatalf("paradigm=orchestrator 应包成 Orchestrator")
	}
}

// Test_F36_ParseEvalLine 覆盖评估解析：分数在前，解析不出来要报错而不是猜 0 分。
func Test_F36_ParseEvalLine(t *testing.T) {
	t.Parallel()

	score, reason, err := parseEvalLine("0.85 回答完整")
	if err != nil || score != 0.85 || reason != "回答完整" {
		t.Fatalf("解析失败: score=%v reason=%q err=%v", score, reason, err)
	}
	if score, _, err := parseEvalLine("1.5 超范围"); err != nil || score != 1 {
		t.Fatalf("超范围分数应被夹到 1: score=%v err=%v", score, err)
	}
	if _, _, err := parseEvalLine("我觉得还行"); err == nil {
		t.Fatalf("无法解析时应报错（否则会把 0 分当成质量差，触发无意义重试）")
	}
	if _, _, err := parseEvalLine("   "); err == nil || !strings.Contains(err.Error(), "空") {
		t.Fatalf("空输出应报错: %v", err)
	}
}
