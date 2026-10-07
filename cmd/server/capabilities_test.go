package main

import (
	"context"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/retrieval"
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
// Test_F79_SummaryTreeIsNotClaimedWhenRetrievalIsOff 钉住一处"清单撒谎"。
//
// wrapHistoryWithRetrieval 在 retrieval.enabled=false 时直接返回原存储——
// 不会套 Hybrid，更不会 WithTree。所以此时摘要树**根本没有被装配**。
//
// 但清单里曾经还有一条按 cfg 判断的分支（cfg.Retrieval.Tree.EffectiveEnabled()），
// 于是 retrieval 关着、tree 单独开着就会报出一个不存在的能力——
// 正是 F-79 要防的"清单描述了不存在的东西"。
func Test_F79_SummaryTreeIsNotClaimedWhenRetrievalIsOff(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Retrieval.Enabled = ptr(false)
	cfg.Retrieval.Tree.Enabled = ptr(true)

	// 装配事实：retrieval 关闭 ⇒ 没有 Hybrid ⇒ 没有摘要树。
	facts := capabilityInputs{TransportMode: "wsclient", History: history.NewMemory(10)}
	if got := countCapability(assembleCapabilities(facts), "summary-tree"); got != 0 {
		t.Fatalf("retrieval 关闭时不该声称有摘要树（出现 %d 次）", got)
	}

	// 对照：retrieval 与 tree 都开着、并且真的套上了 Hybrid 时才应出现。
	cfg.Retrieval.Enabled = ptr(true)
	hybrid := history.NewHybrid(history.NewMemory(10), retrieval.HybridConfig{}).
		WithTree(&history.TreeConfig{}, nil)
	facts.History = hybrid
	if got := countCapability(assembleCapabilities(facts), "summary-tree"); got != 1 {
		t.Fatalf("真的装了摘要树时应恰好出现一次（出现 %d 次）", got)
	}
}

func Test_F79_CapabilityListReflectsActualWiring(t *testing.T) {
	t.Parallel()

	// 最小装配：只有内核类能力。
	bare := assembleCapabilities(capabilityInputs{TransportMode: "wsclient"})
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

	// 完整装配：每一项都必须出现且只出现一次。
	// 注意这里不再需要动 cfg——清单只认**装配事实**，所以"装了什么"完全由
	// capabilityInputs 表达。这正是把 *config.Config 从签名里去掉的意义：
	// 读不到配置，也就不可能再报出配置想要、但实际没装上的能力。
	pol, err := policy.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	hybrid := history.NewHybrid(history.NewMemory(10), retrieval.HybridConfig{}).
		WithTree(&history.TreeConfig{}, nil)
	if err := hybrid.Append(context.Background(), "k", history.Item{Kind: history.KindUser, Content: "x"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	full := assembleCapabilities(capabilityInputs{
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
		RateLimit:       true,
		Singleflight:    true,
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
