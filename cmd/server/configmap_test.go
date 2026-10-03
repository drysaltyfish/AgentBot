package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/admin"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/router"
)

func testLogger(t *testing.T) *observe.Logger {
	t.Helper()
	lg := observe.New(observe.Options{Level: "error", Format: "json", QueueSize: 64, Writer: io.Discard})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	})
	return lg
}

func ptr[T any](v T) *T { return &v }

// Test_F26_ProviderNameAndBaseURL 覆盖 provider 名归一与默认端点选择。
func Test_F26_ProviderNameAndBaseURL(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.LLM.Provider = "  DeepSeek "
	if got := providerName(cfg); got != "deepseek" {
		t.Fatalf("providerName: actual=%q", got)
	}
	cfg.LLM.Provider = ""
	if got := providerName(cfg); got != "openai" {
		t.Fatalf("空 provider 应回落到 openai: %q", got)
	}
	cfg.LLM.BaseURL = ""
	if got := baseURL(cfg, "deepseek"); got != "https://api.deepseek.com" {
		t.Fatalf("deepseek 默认端点: %q", got)
	}
	if got := baseURL(cfg, "openai"); got != "https://api.openai.com/v1" {
		t.Fatalf("openai 默认端点: %q", got)
	}
	cfg.LLM.BaseURL = "https://example.com/v1"
	if got := baseURL(cfg, "deepseek"); got != "https://example.com/v1" {
		t.Fatalf("显式端点应被尊重: %q", got)
	}
}

// Test_F13_ReplyRuleAndShape 覆盖回复策略与发送形态的配置映射。
func Test_F13_ReplyRuleAndShape(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	rule := replyRule(cfg)

	private := router.NewCtx(context.Background(), &event.Event{UserID: 2, SelfID: 1}, nil)
	if !rule(private) {
		t.Fatalf("私聊默认应回复")
	}
	group := router.NewCtx(context.Background(), &event.Event{UserID: 2, GroupID: 3, SelfID: 1}, nil)
	if rule(group) {
		t.Fatalf("群聊未 @ 时默认不应回复")
	}
	self := router.NewCtx(context.Background(), &event.Event{UserID: 1, SelfID: 1}, nil)
	if rule(self) {
		t.Fatalf("不应回复机器人自己")
	}

	shape := sendShapeOf(cfg)
	if !shape.SplitOnBlank || shape.MaxSegments < 1 {
		t.Fatalf("默认发送形态不符: %+v", shape)
	}
	cfg.Behavior.SplitDelay = &config.Duration{D: 0}
	if got := sendShapeOf(cfg); got.Delay != 0 {
		t.Fatalf("显式 0 间隔应保留: %v", got.Delay)
	}
}

// Test_F45_AgentRoleAndIdentity 覆盖角色映射与发言人标识。
func Test_F45_AgentRoleAndIdentity(t *testing.T) {
	t.Parallel()
	// 用指针：event.Event 内含 sync.Once，按值拷贝会被 vet 拦下。
	roleCases := []struct {
		ev   *event.Event
		want agent.Role
	}{
		{&event.Event{GroupID: 0}, agent.RolePrivate},
		{&event.Event{GroupID: 1, Sender: event.Sender{Role: "owner"}}, agent.RoleOwner},
		{&event.Event{GroupID: 1, Sender: event.Sender{Role: "admin"}}, agent.RoleAdmin},
		{&event.Event{GroupID: 1, Sender: event.Sender{Role: "member"}}, agent.RoleMember},
		{&event.Event{GroupID: 1}, agent.RoleMember},
	}
	for _, tc := range roleCases {
		if got := agentRole(tc.ev); got != tc.want {
			t.Fatalf("agentRole(group=%d role=%q): actual=%q want=%q", tc.ev.GroupID, tc.ev.Sender.Role, got, tc.want)
		}
	}

	if got := groupScopedUserID(0, 5); got != 0 {
		t.Fatalf("非正用户号应为 0: %d", got)
	}
	if got := groupScopedUserID(7, 0); got != 0 {
		t.Fatalf("私聊不标发言人: %d", got)
	}
	if got := groupScopedUserID(7, 5); got != 7 {
		t.Fatalf("群聊应以 QQ 号为锚点: %d", got)
	}
	if got := speakerDisplayName(event.Sender{Card: " 群名片 ", Nickname: "昵称"}, 5); got != "群名片" {
		t.Fatalf("群名片优先: %q", got)
	}
	if got := speakerDisplayName(event.Sender{Nickname: "昵称"}, 5); got != "昵称" {
		t.Fatalf("无群名片时用昵称: %q", got)
	}
	if got := speakerDisplayName(event.Sender{Nickname: "昵称"}, 0); got != "" {
		t.Fatalf("私聊不渲染名字: %q", got)
	}
}

// Test_F61_ResolveAPIKeyPriority 覆盖密钥来源优先级在组合根的表现。
func Test_F61_ResolveAPIKeyPriority(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.APIKey = ptr("inline-key")
	cfg.LLM.APIKeyEnv = nil
	cfg.LLM.APIKeyFile = nil
	got, warn, err := resolveAPIKey(cfg)
	if err != nil || got != "inline-key" || warn == "" {
		t.Fatalf("内联应被采用并告警: %q warn=%q err=%v", got, warn, err)
	}

	t.Setenv("AGENTBOT_TEST_KEY", "env-key")
	cfg.LLM.APIKeyEnv = ptr("AGENTBOT_TEST_KEY")
	got, warn, err = resolveAPIKey(cfg)
	if err != nil || got != "env-key" || warn != "" {
		t.Fatalf("环境变量应优先且无告警: %q warn=%q err=%v", got, warn, err)
	}
}

// Test_F68_OutboundAuditHookCountsGuardBlocks 覆盖出口丢弃到指标。
func Test_F68_OutboundAuditHookCountsGuardBlocks(t *testing.T) {
	t.Parallel()
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	hook := outboundAuditHook(cat, testLogger(t))
	hook(outbound.AuditRecord{Dropped: true, Reason: "chain", Filtered: "x"})
	hook(outbound.AuditRecord{Dropped: false})

	var b strings.Builder
	if err := cat.Registry.WritePrometheus(&b); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(b.String(), "guard_blocks_total") {
		t.Fatalf("应记录 guard_blocks: %s", b.String())
	}
}

// Test_F18_RateLimitedHookRecordsMetricsAndAudit 覆盖限速拒绝的指标与审计。
func Test_F18_RateLimitedHookRecordsMetricsAndAudit(t *testing.T) {
	t.Parallel()
	cat := metrics.NewCatalog(metrics.CatalogOptions{})
	var buf bytes.Buffer
	alog := audit.New(audit.Options{Writer: &buf, QueueSize: 8, Now: time.Now})
	hook := rateLimitedHook(cat, alog, "user")

	c := router.NewCtx(context.Background(), &event.Event{UserID: 5, GroupID: 6}, nil)
	hook(c)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := alog.Close(ctx); err != nil {
		t.Fatalf("audit Close: %v", err)
	}
	if !strings.Contains(buf.String(), "rate_limited") {
		t.Fatalf("审计应记录 rate_limited: %s", buf.String())
	}

	var b strings.Builder
	_ = cat.Registry.WritePrometheus(&b)
	if !strings.Contains(b.String(), "rate_limited_total") {
		t.Fatalf("指标应记录 rate_limited_total: %s", b.String())
	}
}

// Test_F60_ApprovalAuditHookDistinguishesDenial 覆盖审批/拒绝两类审计。
func Test_F60_ApprovalAuditHookDistinguishesDenial(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	alog := audit.New(audit.Options{Writer: &buf, QueueSize: 8, Now: time.Now})
	hook := approvalAuditHook(alog)

	hook(agent.ApprovalRecord{Allowed: true, Request: agent.ApprovalRequest{ToolName: "echo"}})
	hook(agent.ApprovalRecord{Allowed: false, Reason: "未放行", Request: agent.ApprovalRequest{ToolName: "danger"}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := alog.Close(ctx); err != nil {
		t.Fatalf("audit Close: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "approval") || !strings.Contains(out, "policy_denied") {
		t.Fatalf("应区分审批与拒绝: %s", out)
	}
}

// Test_F19_NewTogglesDefaults 覆盖功能开关的启用与默认状态。
func Test_F19_NewTogglesDefaults(t *testing.T) {
	t.Parallel()
	lg := testLogger(t)

	cfg := config.Default()
	cfg.Toggle.Enabled = nil
	disabled := newToggles(cfg, lg)
	if !disabled.IsOn("reply", 1) {
		t.Fatalf("未启用开关时应视为全开")
	}

	cfg.Toggle.Enabled = ptr(true)
	cfg.Toggle.File = ""
	cfg.Toggle.DefaultOn = ptr(true)
	toggles := newToggles(cfg, lg)
	if err := toggles.Set("reply", 42, false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if toggles.IsOn("reply", 42) {
		t.Fatalf("显式关闭后应为关")
	}
	if !toggles.IsOn("reply", 43) {
		t.Fatalf("其他群不受影响")
	}
}

// Test_ConfigMapScalarHelpers 覆盖组合根里剩下的标量辅助。
func Test_ConfigMapScalarHelpers(t *testing.T) {
	t.Parallel()
	if got := stringOr(nil, "fallback"); got != "fallback" {
		t.Fatalf("stringOr(nil): %q", got)
	}
	if got := stringOr(ptr("v"), "fallback"); got != "v" {
		t.Fatalf("stringOr: %q", got)
	}
	if got := floatOr(nil, 1.5); got != 1.5 {
		t.Fatalf("floatOr(nil): %v", got)
	}
	if got := floatOr(ptr(2.5), 1.5); got != 2.5 {
		t.Fatalf("floatOr: %v", got)
	}
	if got := errText(nil); got != "" {
		t.Fatalf("errText(nil) 应为空: %q", got)
	}
	if got := errText(io.EOF); got != io.EOF.Error() {
		t.Fatalf("errText: %q", got)
	}
	if got := pick(true, "x"); got != "x" {
		t.Fatalf("pick(true): %q", got)
	}
	if got := pick(false, "x"); got != "" {
		t.Fatalf("pick(false): %q", got)
	}
}

// Test_F46_SandboxPolicyFromConfig 覆盖 F-46 的接线映射与启动期校验：
// 默认关闭不改行为；开启后配置真的进到策略里；非法策略在启动期就失败。
func Test_F46_SandboxPolicyFromConfig(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	if _, on, err := sandboxPolicyFromConfig(cfg); on || err != nil {
		t.Fatalf("默认应关闭且无错误: on=%v err=%v", on, err)
	}

	cfg.Sandbox.Enabled = ptr(true)
	cfg.Sandbox.MaxOutputBytes = ptr(1024)
	cfg.Sandbox.ReadRoots = []string{t.TempDir()}
	cfg.Sandbox.AllowNetwork = ptr(true)
	cfg.Sandbox.ForbiddenTools = []string{"exec"}
	p, on, err := sandboxPolicyFromConfig(cfg)
	if err != nil || !on {
		t.Fatalf("应启用且无错误: on=%v err=%v", on, err)
	}
	if p.MaxOutputBytes != 1024 || !p.AllowNetwork || len(p.ReadRoots) != 1 || len(p.ForbiddenTools) != 1 {
		t.Fatalf("配置映射不符: %+v", p)
	}

	// 边界：相对路径白名单必须在启动期被拒（否则运行时才炸，且判定含糊）。
	cfg.Sandbox.ReadRoots = []string{"relative/path"}
	if _, _, err := sandboxPolicyFromConfig(cfg); err == nil {
		t.Fatalf("相对路径白名单应在启动期报错")
	}
}

// Test_F66_ObservedLLMRecordsCost 覆盖 F-66 的接线：LLM 装饰器把真实 usage 记进成本器。
// 注意 cat 传 nil——这同时验证"记账不依赖指标目录"，避免 cat 为空时静默丢账。
func Test_F66_ObservedLLMRecordsCost(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Cost.Enabled = ptr(true)
	cfg.Cost.Prices = map[string]config.CostPrice{
		"test-model": {InputPer1K: 0.01, OutputPer1K: 0.02},
	}
	tracker, err := buildCostTracker(cfg, testLogger(t), nil)
	if err != nil {
		t.Fatalf("buildCostTracker: %v", err)
	}
	defer func() { _ = tracker.Close() }()

	obs := &observedLLM{
		next:     stubLLM{resp: &llm.ChatResponse{Usage: llm.Usage{PromptTokens: 1000, CompletionTokens: 500}}},
		provider: "test",
		model:    "test-model",
		cost:     tracker,
	}
	if _, err := obs.Chat(context.Background(), &llm.ChatRequest{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	agg := tracker.Global()
	if agg.Calls != 1 {
		t.Fatalf("应记录 1 次调用: %+v", agg)
	}
	if agg.Cost <= 0 {
		t.Fatalf("应按价格表算出费用: %+v", agg)
	}
}

type stubLLM struct{ resp *llm.ChatResponse }

func (s stubLLM) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return s.resp, nil
}

func (s stubLLM) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.Chunk, error) {
	return nil, nil
}

// Test_F71_AdminModuleAuthorizesAndAudits 覆盖 F-71 的接线：
// 超管能执行、非超管被拒、两种情形都留下审计。
func Test_F71_AdminModuleAuthorizesAndAudits(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Moderation.SuperUsers = []int64{42}

	var buf bytes.Buffer
	alog := audit.New(audit.Options{Writer: &buf, QueueSize: 16, Now: time.Now})
	m := buildAdminModule(cfg, alog, testLogger(t), nil)

	if _, err := m.Dispatch(context.Background(), admin.Request{Text: "/help", UserID: 42, Source: admin.SourceMessage}); err != nil {
		t.Fatalf("超管执行 /help 不应报错: %v", err)
	}
	reply, err := m.Dispatch(context.Background(), admin.Request{Text: "/help", UserID: 7, Source: admin.SourceMessage})
	if err != nil {
		t.Fatalf("未授权不应返回错误（应走 DenyExplicit 回复）: %v", err)
	}
	if reply == "" {
		t.Fatalf("DenyExplicit 模式下未授权应得到明确拒绝回复")
	}

	// 未知命令不能被管理模块吞掉（否则普通聊天里的斜杠会消失）。
	if _, err := m.Dispatch(context.Background(), admin.Request{Text: "/不存在的命令", UserID: 42}); !errors.Is(err, admin.ErrUnknownCommand) {
		t.Fatalf("未知命令应返回 ErrUnknownCommand: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := alog.Close(ctx); err != nil {
		t.Fatalf("audit Close: %v", err)
	}
	if !strings.Contains(buf.String(), "admin_command") {
		t.Fatalf("应记录 admin_command 审计: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "denied") {
		t.Fatalf("被拒调用也必须留痕: %s", buf.String())
	}
}
