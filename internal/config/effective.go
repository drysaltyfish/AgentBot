package config

import "time"

// 本文件的取值访问器把“未设置（nil 或零值）→ 模块默认”的解析收进模块内部，
// 调用方直接用 cfg.X.Y() 读取生效值，不必再抄一份默认常量。
//
// 默认值来自 defaults.go 里的常量；默认值属于其他包（store / agent / conversation）
// 的字段保留显式 fallback 参数，由调用方传入那个包的常量，避免跨包复制默认值。

// orPositive 解析“nil 或非正即取默认”的整数字段。
func orPositive(p *int, fallback int) int {
	if p == nil || *p <= 0 {
		return fallback
	}
	return *p
}

// orBool 解析“nil 即取默认”的布尔字段。
func orBool(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

// BusyTimeoutOr 返回 SQLite 忙等待超时；未配置或非正时取 fallback（通常为 store.DefaultBusyTimeout）。
func (s Store) BusyTimeoutOr(fallback time.Duration) time.Duration { return s.BusyTimeout.Or(fallback) }

// EffectiveRetention 返回历史存储保留的条目上限；未配置或小于 1 时取默认 400。
func (h History) EffectiveRetention() int { return orPositive(h.Retention, defaultHistoryRetention) }

// MaxIterationsOr 返回 ReAct 循环迭代上限；未配置或非正时取 fallback（通常为 agent.DefaultMaxIterations）。
func (a Agent) MaxIterationsOr(fallback int) int { return orPositive(a.MaxIterations, fallback) }

// StepTimeoutOr 返回单步工具执行超时；未配置或非正时取 fallback（通常为 agent.DefaultStepTimeout）。
func (a Agent) StepTimeoutOr(fallback time.Duration) time.Duration { return a.StepTimeout.Or(fallback) }

// ApprovalTimeoutOr 返回审批等待预算；未配置或非正时取 fallback（通常为 agent.DefaultApprovalTimeout）。
func (a Agent) ApprovalTimeoutOr(fallback time.Duration) time.Duration {
	return a.ApprovalTimeout.Or(fallback)
}

// EffectiveMemory 返回是否启用进程内长期记忆；未配置时默认开启。
func (a Agent) EffectiveMemory() bool { return orBool(a.Memory, true) }

// EffectiveVirtualActions 返回是否注册 end_action / save_memory / noop；未配置时默认开启。
func (a Agent) EffectiveVirtualActions() bool { return orBool(a.VirtualActions, true) }

// EffectiveMemoryMax 返回每个作用域的记忆条数上限；未配置或小于 1 时取默认 64。
func (a Agent) EffectiveMemoryMax() int { return orPositive(a.MemoryMax, defaultAgentMemoryMax) }

// EffectiveEnabled 返回语义判官是否启用；未配置时默认开启。
func (m MemoryJudge) EffectiveEnabled() bool { return orBool(m.Enabled, true) }

// EffectiveEnabled 返回主动记忆通道是否启用；未配置时默认开启。
func (p ProactiveMemory) EffectiveEnabled() bool { return orBool(p.Enabled, true) }

// EffectiveEnabled 返回工具使用提示是否启用；未配置时默认开启。
func (t ToolHint) EffectiveEnabled() bool { return orBool(t.Enabled, true) }

// EffectivePrivate 返回私聊回复策略；未配置时默认 always。
func (b Behavior) EffectivePrivate() string {
	if b.Private == "" {
		return ReplyAlways
	}
	return b.Private
}

// EffectiveGroup 返回群聊回复策略；未配置时默认 on_mention。
func (b Behavior) EffectiveGroup() string {
	if b.Group == "" {
		return ReplyOnMention
	}
	return b.Group
}

// EffectiveSplitOnBlankLine 返回是否按空行拆分回复；未配置时默认开启。
func (b Behavior) EffectiveSplitOnBlankLine() bool { return orBool(b.SplitOnBlankLine, true) }

// EffectiveSplitDelay 返回连发间隔；未配置时取默认 400ms。
//
// 显式 0 会被保留：0 是合法取值（表示连发之间不等待），与“未配置”不同。
func (b Behavior) EffectiveSplitDelay() time.Duration {
	if b.SplitDelay == nil {
		return defaultBehaviorSplitDelay
	}
	return b.SplitDelay.D
}

// EffectiveMaxSegments 返回单次回复最多拆成几条；未配置或小于 1 时取默认 4。
func (b Behavior) EffectiveMaxSegments() int {
	return orPositive(b.MaxSegments, defaultBehaviorMaxSegments)
}

// EffectiveSelfID 返回机器人自身 QQ 号；未配置时为 0（未知）。
func (t Transport) EffectiveSelfID() int64 {
	if t.SelfID == nil {
		return 0
	}
	return *t.SelfID
}

// EffectiveTimeout 返回模型请求超时；未配置或非正时取默认 30s。
func (l LLM) EffectiveTimeout() time.Duration { return l.Timeout.Or(defaultLLMTimeout) }

// EffectiveHistoryTurns 返回最多回灌多少条历史；未配置或非正时取默认 20。
func (l LLM) EffectiveHistoryTurns() int { return orPositive(l.HistoryTurns, defaultLLMHistoryTurns) }

// AmbientTokenBudgetOr 返回环境消息的 token 预算；未配置或为 0 时取 fallback。
//
// 负数表示不压缩，是合法取值，会被原样保留。
func (l LLM) AmbientTokenBudgetOr(fallback int) int {
	if l.AmbientTokenBudget == nil || *l.AmbientTokenBudget == 0 {
		return fallback
	}
	return *l.AmbientTokenBudget
}

// AmbientMaxCharsOr 返回单条环境消息的字符上限；未配置或小于 1 时取 fallback。
func (l LLM) AmbientMaxCharsOr(fallback int) int { return orPositive(l.AmbientMaxChars, fallback) }

// EffectiveQueueSize 返回日志队列容量；未配置或非正时取默认 1024。
func (l Log) EffectiveQueueSize() int { return orPositive(l.QueueSize, defaultLogQueueSize) }

// EffectiveTimeout 返回优雅关闭预算；未配置或非正时取默认 10s。
func (s Shutdown) EffectiveTimeout() time.Duration { return s.Timeout.Or(defaultShutdownTimeout) }
