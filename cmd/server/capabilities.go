package main

import (
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
)

// capabilityInputs 是"本次装配到底装了什么"的事实来源。
//
// 刻意用**对象**而不是配置开关：配置说"想要"，装配的结果才决定"真的有"。
// 两者不一致时（例如构建失败后退回降级），这里必须反映真实的那一个。
type capabilityInputs struct {
	TransportMode   string
	AuthConfigured  bool
	Cost            bool
	Budget          *llm.Budget
	Policy          *policyState
	Personas        bool
	Memory          bool
	History         history.History
	Semcache        bool
	Streaming       bool
	Moderation      bool
	Admin           bool
	Toggles         bool
	ProactiveMemory bool
	ToolHint        bool
	PromptEngine    bool
	AuditLog        bool
	OpsHTTP         bool
}

// assembleCapabilities 汇总本次装配真实可达的能力清单（F-79）。
//
// F-79 的验收是"启动日志中能看到已启用能力清单"。清单**必须由装配事实推出**：
// 写死的字符串列表会随每次接线而悄悄过期，最后变成一份描述过去的文档——
// 那正是 F-79 要防的"文档描述了不存在的功能"的镜像版本。
func assembleCapabilities(cfg *config.Config, in capabilityInputs) []string {
	caps := &capabilityList{}
	caps.add("event-kernel", "router-snapshot-match", "rule-handler-separation", "engine-hooks",
		"session-manager", "history-memory", "llm-interface", "llm-retry",
		"outbound-filter-chain", "graceful-shutdown", "metrics", "health-probes",
		"trace-propagation", "session-reclaimer")
	if in.TransportMode != "" {
		caps.add("transport-" + in.TransportMode)
	}
	if in.AuthConfigured {
		caps.add("transport-auth")
	}
	if in.AuditLog {
		caps.add("audit-log")
	}
	if in.PromptEngine {
		caps.add("prompt-templates")
	}
	if in.Policy != nil {
		caps.add("policy-table", "policy-hard-gate", "policy-hot-reload")
	}
	if in.Personas {
		caps.add("personas", "persona-hot-reload")
	}
	if in.Memory {
		caps.add("tiered-memory")
	}
	if in.Cost {
		caps.add("cost-tracking", "cost-quota")
	}
	if in.Budget != nil {
		caps.add("context-budget")
	}
	if h, ok := in.History.(*history.Hybrid); ok {
		caps.add("hybrid-retrieval")
		if h.Tree != nil {
			caps.add("summary-tree")
		}
	}
	if in.Semcache {
		caps.add("semantic-cache")
	}
	if in.Streaming {
		caps.add("streaming-send")
	}
	if in.Moderation {
		caps.add("inbound-moderation")
	}
	if in.Admin {
		caps.add("admin-commands")
	}
	if in.Toggles {
		caps.add("feature-toggle")
	}
	if in.ProactiveMemory {
		caps.add("proactive-memory")
	}
	if in.ToolHint {
		caps.add("tool-hint")
	}
	if cfg != nil && cfg.RateLimit.EffectiveEnabled() {
		caps.add("rate-limit", "rate-limit-hot-reload")
	}
	if cfg != nil && cfg.Singleflight.EffectiveEnabled() {
		caps.add("singleflight")
	}
	if cfg != nil && cfg.Retrieval.Tree.EffectiveEnabled() {
		caps.add("summary-tree")
	}
	if in.OpsHTTP {
		caps.add("ops-http")
	}
	return caps.list()
}

// capabilityList 是有序去重的字符串集合。
type capabilityList struct{ names []string }

func (c *capabilityList) add(names ...string) {
	for _, name := range names {
		if name == "" || c.has(name) {
			continue
		}
		c.names = append(c.names, name)
	}
}

func (c *capabilityList) has(name string) bool {
	for _, n := range c.names {
		if n == name {
			return true
		}
	}
	return false
}

func (c *capabilityList) list() []string { return append([]string(nil), c.names...) }
