package main

import (
	"context"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/policy"
)

// countCapability 返回某能力名在清单里出现的次数（用于钉住去重）。
func countCapability(list []string, name string) int {
	n := 0
	for _, item := range list {
		if item == name {
			n++
		}
	}
	return n
}

// Test_F79_CapabilityListReflectsActualWiring 是 F-79 验收"启动日志里能看到
// 已启用能力清单"的接线测试：清单必须反映**装配事实**，而不是一份固定字符串。
func Test_F79_CapabilityListReflectsActualWiring(t *testing.T) {
	t.Parallel()
	cfg := config.Default()

	// 最小装配：只有内核类能力。
	bare := assembleCapabilities(cfg, capabilityInputs{TransportMode: "wsclient"})
	for _, want := range []string{"event-kernel", "session-manager", "trace-propagation", "session-reclaimer"} {
		if countCapability(bare, want) != 1 {
			t.Fatalf("最小装配缺少 %q: %v", want, bare)
		}
	}
	for _, absent := range []string{"rate-limit", "policy-table", "semantic-cache", "summary-tree", "context-budget", "tiered-memory", "prompt-templates"} {
		if countCapability(bare, absent) != 0 {
			t.Fatalf("未装配的能力不该出现 %q: %v", absent, bare)
		}
	}
	if countCapability(bare, "transport-wsclient") != 1 {
		t.Fatalf("传输形态应进清单: %v", bare)
	}
	if countCapability(bare, "transport-auth") != 0 {
		t.Fatalf("未配置鉴权时不该出现 transport-auth: %v", bare)
	}

	// 完整装配：所有开关打开后，每一项都必须出现且只出现一次。
	cfg.RateLimit.Enabled = ptr(true)
	cfg.Singleflight.Enabled = ptr(true)
	cfg.Toggle.Enabled = ptr(true)
	cfg.Retrieval.Enabled = ptr(true)
	cfg.Retrieval.Tree.Enabled = ptr(true)
	pol, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	hybrid := history.NewHybrid(history.NewMemory(10), memory.HybridConfig{}).
		WithTree(&history.TreeConfig{}, nil)
	if err := hybrid.Append(context.Background(), "k", history.Item{Kind: history.KindUser, Content: "x"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	full := assembleCapabilities(cfg, capabilityInputs{
		TransportMode:   "wsclient",
		AuthConfigured:  true,
		Cost:            true,
		Budget:          &llm.Budget{MaxContext: 4096},
		Policy:          newPolicyState(pol),
		Personas:        true,
		Memory:          true,
		History:         hybrid,
		Semcache:        true,
		Streaming:       true,
		Moderation:      true,
		Admin:           true,
		Toggles:         true,
		ProactiveMemory: true,
		ToolHint:        true,
		PromptEngine:    true,
		AuditLog:        true,
		OpsHTTP:         true,
	})
	for _, want := range []string{
		"transport-auth", "audit-log", "prompt-templates", "policy-table", "policy-hard-gate",
		"personas", "tiered-memory", "cost-tracking", "cost-quota", "context-budget",
		"hybrid-retrieval", "summary-tree", "semantic-cache", "streaming-send",
		"inbound-moderation", "admin-commands", "feature-toggle", "rate-limit",
		"singleflight", "proactive-memory", "tool-hint", "ops-http",
	} {
		if countCapability(full, want) != 1 {
			t.Fatalf("完整装配缺少或重复 %q (%d 次): %v", want, countCapability(full, want), full)
		}
	}
	if !strings.Contains(strings.Join(full, ","), "rate-limit-hot-reload") {
		t.Fatalf("热加载能力应进清单: %v", full)
	}
}
