package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/audit"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/retry"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/tool"
	"github.com/drysaltyfish/agentbot/internal/tool/builtin"
)

// openAIDefaultBase 是 openai provider 的默认端点，用于判断 base_url 是否被显式配置过。
const openAIDefaultBase = "https://api.openai.com/v1"

// buildLLM 按配置选择模型实现。echo 是联调用的假实现。
func buildLLM(cfg *config.Config, lg *observe.Logger) (llm.LLM, error) {
	apiKey, keyWarn, kerr := resolveAPIKey(cfg)
	if kerr != nil {
		return nil, kerr
	}
	if keyWarn != "" {
		lg.Component("llm").Warn(keyWarn)
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	switch provider {
	case "echo":
		lg.Component("llm").Warn("using the echo provider: replies are a fixed template, not a real model")
		return llm.NewEcho(""), nil
	case "", "openai", "deepseek":
		client := httpx.NewClient(httpx.Config{
			Timeout:      cfg.LLM.EffectiveTimeout(),
			MaxBytes:     httpx.Defaults().MaxBytes,
			MaxRedirects: 3,
			// 本地/内网 provider（如 Ollama）需要显式放行；默认按 F-59 拒绝私网。
			AllowPrivate: false,
		})
		base := llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL:            baseURL(cfg, provider),
			APIKey:             apiKey,
			Model:              cfg.LLM.Model,
			Client:             client,
			Thinking:           cfg.LLM.Thinking,
			ReasoningEffort:    cfg.LLM.ReasoningEffort,
			IncludeStreamUsage: true,
		})
		return llm.NewRetryLLM(base, retry.Default()), nil
	default:
		return nil, fmt.Errorf("unsupported llm.provider %q", cfg.LLM.Provider)
	}
}

// buildJudgeLLM 构造**关闭思考**的语义判官客户端（F-87）。
//
// 关闭思考是刻意的：判定只需要一个标签，开着思考会为它多花几百个 token 与几秒延迟。
// 复用同一个 provider 与端点，只是换一组模型参数——因此不需要第二份配置。
func buildJudgeLLM(cfg *config.Config, lg *observe.Logger) (llm.LLM, error) {
	apiKey, keyWarn, kerr := resolveAPIKey(cfg)
	if kerr != nil {
		return nil, kerr
	}
	if keyWarn != "" {
		lg.Component("llm").Warn(keyWarn)
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	switch provider {
	case "echo":
		// 假模型判不了语义；返回 nil 让调用方退回确定性判据。
		lg.Component("memory").Info("echo provider cannot judge semantics; using the deterministic threshold")
		return nil, nil
	case "", "openai", "deepseek":
		client := httpx.NewClient(httpx.Config{
			Timeout:      cfg.LLM.EffectiveTimeout(),
			MaxBytes:     httpx.Defaults().MaxBytes,
			MaxRedirects: 3,
		})
		noThinking := false // 判官不需要思考
		base := llm.NewOpenAI(llm.OpenAIConfig{
			BaseURL:  baseURL(cfg, provider),
			APIKey:   apiKey,
			Model:    cfg.LLM.Model,
			Client:   client,
			Thinking: &noThinking,
		})
		return llm.NewRetryLLM(base, retry.Default()), nil
	default:
		return nil, fmt.Errorf("unsupported llm.provider %q", cfg.LLM.Provider)
	}
}

// baseURL 在未显式配置时给出该 provider 的默认端点。
//
// 注意：config.Default() 会把 base_url 预置成 OpenAI 的地址，所以 deepseek 必须同时
// 识别"为空"与"仍是 OpenAI 默认值"两种情况，否则会静默打到错误的端点。
func baseURL(cfg *config.Config, provider string) string {
	u := strings.TrimSpace(cfg.LLM.BaseURL)
	if provider == "deepseek" && (u == "" || u == openAIDefaultBase) {
		return "https://api.deepseek.com"
	}
	if u == "" {
		return openAIDefaultBase
	}
	return u
}

// systemPrompt 返回不可变前缀正文。
//
// 文件形式在启动时读一次就固定下来——之后任何时刻读文件都可能拿到改动后的内容，
// 那会让前缀在运行中变化，缓存全部失效。
func systemPrompt(cfg *config.Config) (string, error) {
	if path := strings.TrimSpace(stringOr(cfg.LLM.SystemPromptFile, "")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read llm.system_prompt_file %s: %w", path, err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", fmt.Errorf("llm.system_prompt_file %s is empty", path)
		}
		return s, nil
	}
	if cfg.LLM.SystemPrompt != nil && strings.TrimSpace(*cfg.LLM.SystemPrompt) != "" {
		return *cfg.LLM.SystemPrompt, nil
	}
	return conversation.DefaultSystemPrompt, nil
}

// buildAgent 按配置装配 Agent（F-35 + F-41 + F-44 + F-45）。
//
// 返回的是 agent.Agent 接口：未启用 ReAct 时返回 DirectAgent，
// 因此调用方对两条路径完全同形，不需要分支。
func buildAgent(cfg *config.Config, model llm.LLM, asm *conversation.Assembler, hist history.History, st *store.Store, caller builtin.CallerProvider, lg *observe.Logger, auditLog *audit.Logger) (agent.Agent, agent.Memory, error) {
	if !cfg.Agent.Enabled {
		return wrapParadigm(cfg, model, &agent.DirectAgent{LLM: model, Assembler: asm}, lg), nil, nil
	}

	registry := tool.New(tool.WithWarnFunc(func(msg string) {
		lg.Component("tool").Warn(msg)
	}))

	// F-87：记忆落在持久层。
	//
	// 语义判官用**关闭思考**的模型：只在相似度落在歧义带时才问一次，
	// 因此绝大多数字记忆写入不付额外调用。判官不可用时退回确定性判据，
	// 写入照常成功——判官只是把判定做得更准，不是必须依赖。
	maxPerScope := cfg.Agent.EffectiveMemoryMax()
	var judge memory.Judge
	if cfg.Agent.MemoryJudge.EffectiveEnabled() {
		jm, jerr := buildJudgeLLM(cfg, lg)
		switch {
		case jerr != nil:
			lg.Component("lifecycle").Warn("cannot build the memory judge; ambiguous writes use the deterministic threshold", "error", jerr)
		case jm == nil:
			// echo provider：判不了语义，保持 judge 为 nil。
		default:
			judge = memory.NewLLMJudge(jm, func(msg string) { lg.Component("memory").Debug(msg) })
			lg.Component("memory").Info("semantic memory judge is enabled (thinking off)")
		}
	}

	var (
		mem     agent.Memory
		memImpl *memory.Store
		// 注意：不能把 nil 的 *memory.Store 直接塞进接口——那样接口不为 nil，
		// 工具会以为管理能力可用，调用时才炸。
		memAdmin builtin.MemoryAdmin
	)
	if cfg.Agent.EffectiveMemory() {
		memImpl = memory.New(memory.Options{
			Store: st, Judge: judge, MaxPerScope: maxPerScope,
			Warn: func(msg string) { lg.Component("memory").Info(msg) },
		})
		mem = memImpl
		memAdmin = memImpl
		lg.Component("memory").Info("long-term memory is stored in the database",
			"max_per_scope", maxPerScope, "judge", judge != nil)
	}

	// 一次性迁移：旧记忆文件导入（幂等）。
	if legacy := strings.TrimSpace(cfg.Agent.MemoryFile); legacy != "" && mem != nil {
		migCtx, cancelMemMig := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelMemMig()
		imported, skipped, ierr := st.ImportMemoriesJSONL(migCtx, legacy)
		switch {
		case errors.Is(ierr, store.ErrImportSourceMissing):
			lg.Component("memory").Info("no legacy memory file to import", "path", legacy)
		case ierr != nil:
			lg.Component("lifecycle").Warn("legacy memory import failed", "error", ierr, "path", legacy)
		default:
			lg.Component("memory").Info("legacy JSONL memory imported",
				"path", legacy, "imported", imported, "skipped", skipped)
		}
	}

	// 内置工具：先全量注册再按配置裁剪，这样顺序始终等于内置顺序（前缀缓存需要稳定）。
	deps := builtin.Deps{Memory: mem, HTTP: httpx.Defaults(), Now: time.Now, History: hist, MemoryAdmin: memAdmin, Caller: caller}
	if err := builtin.Register(registry, deps); err != nil {
		return nil, nil, fmt.Errorf("register builtin tools: %w", err)
	}
	if len(cfg.Agent.Tools) > 0 {
		keep := map[string]bool{}
		for _, n := range cfg.Agent.Tools {
			keep[n] = true
		}
		for _, n := range registry.Names() {
			if !keep[n] {
				registry.Remove(n)
			}
		}
	}

	if cfg.Agent.EffectiveVirtualActions() {
		if err := agent.RegisterVirtual(registry, mem); err != nil {
			return nil, nil, fmt.Errorf("register virtual actions: %w", err)
		}
	}
	// F-46：工具执行沙箱（默认关闭）。放在装配最后一步，
	// 保证"注册顺序 = 内置顺序"不被破坏（前缀缓存依赖它）。
	if policy, on, perr := sandboxPolicyFromConfig(cfg); perr != nil {
		return nil, nil, perr
	} else if on {
		sandboxed, serr := registry.Sandbox(&policy)
		if serr != nil {
			return nil, nil, fmt.Errorf("apply tool sandbox: %w", serr)
		}
		registry = sandboxed
		lg.Component("tool").Info("tool sandbox enabled",
			"tools", len(registry.Names()),
			"max_output_bytes", policy.MaxOutputBytes,
			"allow_network", policy.AllowNetwork,
			"require_read_only", policy.RequireReadOnly)
	}

	react := &agent.ReactAgent{
		LLM:             model,
		Tools:           registry,
		Assembler:       asm,
		MaxIterations:   cfg.Agent.MaxIterationsOr(agent.DefaultMaxIterations),
		StepTimeout:     cfg.Agent.StepTimeoutOr(agent.DefaultStepTimeout),
		Protocol:        agent.Protocol(strings.ToLower(strings.TrimSpace(cfg.Agent.Protocol))),
		Memory:          mem,
		ApprovalTimeout: cfg.Agent.ApprovalTimeoutOr(agent.DefaultApprovalTimeout),
		Warn:            func(msg string) { lg.Component("agent").Warn(msg) },
		// F-60：审批与策略拒绝都要留痕。
		OnApproval: approvalAuditHook(auditLog),
	}

	if cfg.Agent.ApprovalEnabled {
		gate := agent.NewTableGate()
		for toolName, roles := range cfg.Agent.Allow {
			for _, r := range roles {
				gate.Set(toolName, agent.Role(r), agent.VerdictAllow)
			}
		}
		react.Gate = gate
		// M2 尚未接入交互式审批通道：未放行的调用会被明确拒绝并回灌原因，
		// 而不是静默放行——这是 fail-closed 的正确表现。
		react.Approver = agent.ApproverFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, error) {
			return agent.Decision{}, fmt.Errorf("该部署未配置人工审批通道")
		})
		lg.Component("agent").Warn("tool approval is enabled but no interactive approver is wired; non-allowed calls will be denied")
	}

	lg.Component("agent").Info("react agent enabled",
		"tools", registry.Names(),
		"max_iterations", react.MaxIterations,
		"protocol", string(react.Protocol),
		"step_timeout", react.StepTimeout.String(),
		"memory", mem != nil,
		"memory_file", strings.TrimSpace(cfg.Agent.MemoryFile),
		"history_file", strings.TrimSpace(cfg.History.File),
		"approval", cfg.Agent.ApprovalEnabled)
	return wrapParadigm(cfg, model, react, lg), mem, nil
}

// sandboxPolicyFromConfig 把配置映射成工具沙箱策略（F-46）。
//
// 返回 on=false 表示未启用；策略非法时返回错误，让启动在配置写错时就失败，
// 而不是等到某个工具被调用时才发现白名单是相对路径。
func sandboxPolicyFromConfig(cfg *config.Config) (tool.Policy, bool, error) {
	if !cfg.Sandbox.EffectiveEnabled() {
		return tool.Policy{}, false, nil
	}
	p := tool.Policy{
		MaxOutputBytes:  cfg.Sandbox.EffectiveMaxOutputBytes(),
		ReadRoots:       cfg.Sandbox.ReadRoots,
		WriteRoots:      cfg.Sandbox.WriteRoots,
		EnvAllowlist:    cfg.Sandbox.EnvAllowlist,
		ForbiddenOps:    cfg.Sandbox.ForbiddenOps,
		ForbiddenTools:  cfg.Sandbox.ForbiddenTools,
		NetworkTools:    cfg.Sandbox.NetworkTools,
		AllowNetwork:    cfg.Sandbox.EffectiveAllowNetwork(),
		RequireReadOnly: cfg.Sandbox.EffectiveRequireReadOnly(),
	}
	if err := p.Validate(); err != nil {
		return tool.Policy{}, false, fmt.Errorf("sandbox policy: %w", err)
	}
	return p, true, nil
}
