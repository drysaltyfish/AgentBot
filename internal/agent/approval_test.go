package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

func approvalLoop(t *testing.T, calls []llm.ToolCall) *scriptedLLM {
	t.Helper()
	return &scriptedLLM{replies: []*llm.ChatResponse{
		{ToolCalls: calls, FinishReason: llm.FinishReasonToolCalls},
		{Content: "继续", FinishReason: "stop"},
	}}
}

func lastToolObservation(t *testing.T, fake *scriptedLLM) llm.Message {
	t.Helper()
	msgs := fake.request(1).Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleTool {
			return msgs[i]
		}
	}
	t.Fatalf("没有找到 tool observation")
	return llm.Message{}
}

// Test_F45_DenyPreventsExecution 覆盖"被拒工具不执行且回灌明确原因"。
func Test_F45_DenyPreventsExecution(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleMember, VerdictDeny)

	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)})
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, et), Gate: gate, Assembler: testAssembler("")}

	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember}); err != nil {
		t.Fatalf("拒绝不应中断循环: %v", err)
	}
	if et.calls != 0 {
		t.Fatalf("被拒工具不应被执行: %d", et.calls)
	}
	if obs := lastToolObservation(t, fake); !strings.Contains(obs.Content, "拒绝") {
		t.Fatalf("拒绝原因必须回灌: %q", obs.Content)
	}
}

// Test_F45_FailClosedUnknownToolNeedsApproval 守住 fail-closed。
func Test_F45_FailClosedUnknownToolNeedsApproval(t *testing.T) {
	t.Parallel()
	gate := NewTableGate() // 空表
	if got := gate.Check(ApprovalRequest{ToolName: "echo", Role: RoleMember}); got != VerdictApprove {
		t.Fatalf("表里没有的工具应判定为需要审批: %v", got)
	}
	gate.Set("echo", RoleAdmin, VerdictAllow)
	if got := gate.Check(ApprovalRequest{ToolName: "echo", Role: RoleMember}); got != VerdictApprove {
		t.Fatalf("该角色没有配置时应判定为需要审批: %v", got)
	}
	if got := gate.Check(ApprovalRequest{ToolName: "echo", Role: RoleAdmin}); got != VerdictAllow {
		t.Fatalf("已配置的角色应按配置: %v", got)
	}
}

// Test_F45_NoApproverMeansDenied 覆盖"需要审批但没通道"。
func Test_F45_NoApproverMeansDenied(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleMember, VerdictApprove)

	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)})
	a := &ReactAgent{LLM: fake, Tools: newRegistry(t, et), Gate: gate, Assembler: testAssembler("")}

	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if et.calls != 0 {
		t.Fatalf("没有审批通道时不应执行: %d", et.calls)
	}
	if obs := lastToolObservation(t, fake); !strings.Contains(obs.Content, "审批") {
		t.Fatalf("应回灌原因: %q", obs.Content)
	}
}

func Test_F45_ApprovedCallExecutes(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleMember, VerdictApprove)

	var records []ApprovalRecord
	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"yes"}`)})
	a := &ReactAgent{
		Assembler: testAssembler(""),
		LLM:       fake, Tools: newRegistry(t, et), Gate: gate,
		Approver: ApproverFunc(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
			return Decision{Allowed: true, Reason: "群主同意"}, nil
		}),
		OnApproval: func(r ApprovalRecord) { records = append(records, r) },
	}
	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if et.calls != 1 {
		t.Fatalf("审批通过后应执行一次: %d", et.calls)
	}
	if obs := lastToolObservation(t, fake); !strings.Contains(obs.Content, "yes") {
		t.Fatalf("应回灌真实执行结果: %q", obs.Content)
	}
	if len(records) != 1 || !records[0].Allowed || records[0].Verdict != VerdictApprove {
		t.Fatalf("审批必须进审计: %+v", records)
	}
}

func Test_F45_ApprovalRejectionIsFedBack(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleMember, VerdictApprove)

	var records []ApprovalRecord
	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)})
	a := &ReactAgent{
		Assembler: testAssembler(""),
		LLM:       fake, Tools: newRegistry(t, et), Gate: gate,
		Approver: ApproverFunc(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
			return Decision{Allowed: false, Reason: "群主不同意"}, nil
		}),
		OnApproval: func(r ApprovalRecord) { records = append(records, r) },
	}
	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if et.calls != 0 {
		t.Fatalf("被拒后不应执行: %d", et.calls)
	}
	if obs := lastToolObservation(t, fake); !strings.Contains(obs.Content, "群主不同意") {
		t.Fatalf("拒绝原因必须回灌: %q", obs.Content)
	}
	if len(records) != 1 || records[0].Allowed {
		t.Fatalf("审计应记录拒绝: %+v", records)
	}
}

// Test_F45_ApprovalTimeoutDeniesAndContinues 覆盖审批超时。
func Test_F45_ApprovalTimeoutDeniesAndContinues(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleMember, VerdictApprove)

	var records []ApprovalRecord
	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)})
	a := &ReactAgent{
		Assembler: testAssembler(""),
		LLM:       fake, Tools: newRegistry(t, et), Gate: gate,
		ApprovalTimeout: 20 * time.Millisecond,
		Approver: ApproverFunc(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
			<-ctx.Done()
			return Decision{}, ctx.Err()
		}),
		OnApproval: func(r ApprovalRecord) { records = append(records, r) },
	}
	out, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember})
	if err != nil {
		t.Fatalf("审批超时不应中断循环: %v", err)
	}
	if et.calls != 0 {
		t.Fatalf("审批超时不应执行: %d", et.calls)
	}
	if out.Text != "继续" {
		t.Fatalf("循环应继续并拿到后续回答: %q", out.Text)
	}
	if len(records) != 1 || !records[0].TimedOut {
		t.Fatalf("审计应标记超时: %+v", records)
	}
}

// slowGateTool 用于验证"审批等待不占用单步超时"。
type slowGateTool struct {
	mu    sync.Mutex
	calls int
}

func (s *slowGateTool) Name() string        { return "slow" }
func (s *slowGateTool) Description() string { return "慢工具" }
func (s *slowGateTool) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{}}
}
func (s *slowGateTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	// 工具本身只花 5ms，远低于下面设置的 40ms 单步超时。
	time.Sleep(5 * time.Millisecond)
	return tool.Success("done"), nil
}

// Test_F45_ApprovalWaitDoesNotEatStepTimeout 是 F-45 最容易做错的一条。
//
// 单步超时设成 40ms，审批等待 120ms。如果审批被放进单步预算里，
// 审批通过后工具会立刻超时——那正是这条验收要防的。
func Test_F45_ApprovalWaitDoesNotEatStepTimeout(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("slow", RoleMember, VerdictApprove)

	probe := &slowGateTool{}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "slow", "{}")})
	a := &ReactAgent{
		Assembler: testAssembler(""),
		LLM:       fake, Tools: newRegistry(t, probe), Gate: gate,
		StepTimeout: 40 * time.Millisecond,
		Approver: ApproverFunc(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
			time.Sleep(120 * time.Millisecond) // 比单步超时长得多
			return Decision{Allowed: true}, nil
		}),
	}

	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleMember}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if probe.calls != 1 {
		t.Fatalf("审批通过后工具应执行: %d", probe.calls)
	}
	obs := lastToolObservation(t, fake)
	if strings.Contains(obs.Content, "timed out") {
		t.Fatalf("审批等待不应吃掉单步超时预算，工具被误判为超时: %q", obs.Content)
	}
	if !strings.Contains(obs.Content, "done") {
		t.Fatalf("应回灌真实结果: %q", obs.Content)
	}
}

func Test_F45_AllowBypassesApprover(t *testing.T) {
	t.Parallel()
	gate := NewTableGate()
	gate.Set("echo", RoleOwner, VerdictAllow)

	called := false
	et := &echoTool{name: "echo"}
	fake := approvalLoop(t, []llm.ToolCall{toolCall("c1", "echo", `{"v":"x"}`)})
	a := &ReactAgent{
		Assembler: testAssembler(""),
		LLM:       fake, Tools: newRegistry(t, et), Gate: gate,
		Approver: ApproverFunc(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
			called = true
			return Decision{Allowed: true}, nil
		}),
	}
	if _, err := a.Run(context.Background(), Input{Query: "x", Role: RoleOwner}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called {
		t.Fatalf("VerdictAllow 不应触发审批")
	}
	if et.calls != 1 {
		t.Fatalf("应直接执行: %d", et.calls)
	}
}

func Test_F45_VerdictString(t *testing.T) {
	t.Parallel()
	for v, want := range map[Verdict]string{VerdictAllow: "allow", VerdictDeny: "deny", VerdictApprove: "approve"} {
		if v.String() != want {
			t.Fatalf("Verdict(%d).String()=%q want %q", v, v.String(), want)
		}
	}
	if Verdict(99).String() == "" {
		t.Fatalf("未知判定也应有可读字符串")
	}
}
