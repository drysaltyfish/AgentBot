package config

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/semcache"
)

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

// EffectiveEnabled 返回是否启用限速；未配置时默认关闭。
func (r RateLimit) EffectiveEnabled() bool { return orBool(r.Enabled, false) }

// EffectiveUserPerMinute 返回单用户每分钟可用次数；未配置或非正时取默认 20。
func (r RateLimit) EffectiveUserPerMinute() int {
	return orPositive(r.UserPerMinute, defaultRateLimitUserPerMinute)
}

// EffectiveUserBurst 返回单用户突发容量；未配置或非正时取默认 5。
func (r RateLimit) EffectiveUserBurst() int {
	return orPositive(r.UserBurst, defaultRateLimitUserBurst)
}

// EffectiveGroupPerMinute 返回单群每分钟可用次数；未配置或非正时取默认 120。
func (r RateLimit) EffectiveGroupPerMinute() int {
	return orPositive(r.GroupPerMinute, defaultRateLimitGroupPerMinute)
}

// EffectiveGroupBurst 返回单群突发容量；未配置或非正时取默认 20。
func (r RateLimit) EffectiveGroupBurst() int {
	return orPositive(r.GroupBurst, defaultRateLimitGroupBurst)
}

// EffectiveEnabled 返回是否启用功能开关；未配置时默认关闭。
func (t Toggle) EffectiveEnabled() bool { return orBool(t.Enabled, false) }

// EffectiveDefaultOn 返回未显式设置过时插件的默认状态；未配置时默认开启。
func (t Toggle) EffectiveDefaultOn() bool { return orBool(t.DefaultOn, true) }

// EffectiveQueueSize 返回审计队列容量；未配置或非正时取默认 4096。
func (a Audit) EffectiveQueueSize() int { return orPositive(a.QueueSize, defaultAuditQueueSize) }

// EffectiveContentLimit 返回审计中用户内容保留的字符数；未配置或小于 1 时取默认 20。
func (a Audit) EffectiveContentLimit() int {
	return orPositive(a.ContentLimit, defaultAuditContentLimit)
}

// EffectiveEnabled 返回是否启用指标与探针监听；未配置时默认开启（仅回环）。
func (o Ops) EffectiveEnabled() bool { return orBool(o.Enabled, true) }

// EffectiveAddr 返回监听地址；未配置时取默认 127.0.0.1:9090。
func (o Ops) EffectiveAddr() string {
	if strings.TrimSpace(o.Addr) == "" {
		return defaultOpsAddr
	}
	return o.Addr
}

// EffectiveReadyCacheTTL 返回就绪检查缓存时长；未配置或非正时取默认 10s。
func (o Ops) EffectiveReadyCacheTTL() time.Duration {
	return o.ReadyCacheTTL.Or(defaultOpsReadyCacheTTL)
}

// EffectiveProbeTimeout 返回单次依赖检查超时；未配置或非正时取默认 1s。
func (o Ops) EffectiveProbeTimeout() time.Duration {
	return o.ProbeTimeout.Or(defaultOpsProbeTimeout)
}

// EffectiveEnabled 返回是否启用单飞中间件；未配置时默认关闭。
func (s Singleflight) EffectiveEnabled() bool { return orBool(s.Enabled, false) }

// EffectiveKey 返回单飞粒度；未配置时默认 user_group。
func (s Singleflight) EffectiveKey() string {
	if strings.TrimSpace(s.Key) == "" {
		return defaultSingleflightKey
	}
	return s.Key
}

// EffectiveNotice 返回拒绝时是否回提示；未配置时默认关闭。
func (s Singleflight) EffectiveNotice() bool { return orBool(s.Notice, defaultSingleflightNotice) }

// EffectivePersonasDir 返回人格定义目录（F-82）；未配置时取 prompt.dir 下的 personas。
func (p Prompt) EffectivePersonasDir() string {
	if p.PersonasDir != nil && strings.TrimSpace(*p.PersonasDir) != "" {
		return *p.PersonasDir
	}
	dir := strings.TrimSpace(p.Dir)
	if dir == "" {
		dir = "prompts"
	}
	return filepath.Join(dir, "personas")
}

// EffectiveEnabled 返回是否启用入站审查；未配置时默认关闭。
func (m Moderation) EffectiveEnabled() bool { return orBool(m.Enabled, false) }

// EffectiveAction 返回敏感词命中后的动作；未配置时默认 mask。
func (m Moderation) EffectiveAction() string {
	if strings.TrimSpace(m.Action) == "" {
		return defaultModerationAction
	}
	return m.Action
}

// EffectiveSpamWindow 返回防刷统计窗口；未配置或非正时取默认 10s。
func (m Moderation) EffectiveSpamWindow() time.Duration {
	return m.AntiSpam.Window.Or(defaultModerationSpamWindow)
}

// EffectiveSpamMaxMessages 返回窗口内允许的最大消息数；未配置或非正时取默认 20。
func (m Moderation) EffectiveSpamMaxMessages() int {
	return orPositive(m.AntiSpam.MaxMessages, defaultModerationSpamMaxMessages)
}

// EffectiveEnabled 返回是否启用流式增量发送；未配置时默认关闭。
func (s Stream) EffectiveEnabled() bool { return orBool(s.Enabled, false) }

// EffectiveMaxChars 返回触发发送的长度阈值；未配置或非正时取 llm 默认值。
func (s Stream) EffectiveMaxChars() int { return orPositive(s.MaxChars, llm.DefaultFlushChars) }

// EffectiveFirstMinChars 返回首段最小长度；未配置或非正时取 llm 默认值。
func (s Stream) EffectiveFirstMinChars() int {
	return orPositive(s.FirstMinChars, llm.DefaultFirstMinChars)
}

// EffectiveMaxInterval 返回时间触发阈值；未配置或非正时取 llm 默认值。
func (s Stream) EffectiveMaxInterval() time.Duration {
	if s.MaxInterval == nil || s.MaxInterval.D <= 0 {
		return llm.DefaultFlushInterval
	}
	return s.MaxInterval.D
}

// EffectiveMinInterval 返回发送频率上限；未配置或非正时取 llm 默认值。
func (s Stream) EffectiveMinInterval() time.Duration {
	if s.MinInterval == nil || s.MinInterval.D <= 0 {
		return llm.DefaultFlushInterval
	}
	return s.MinInterval.D
}

// EffectiveEditMessages 返回是否用"编辑消息"表达改写；未配置时默认关闭。
func (s Stream) EffectiveEditMessages() bool { return orBool(s.EditMessages, false) }

// EffectiveEnabled 返回是否启用语义缓存；未配置时默认关闭。
func (s Semcache) EffectiveEnabled() bool { return orBool(s.Enabled, false) }

// EffectiveThreshold 返回命中阈值；未配置或非正时取 semcache 的默认值。
func (s Semcache) EffectiveThreshold() float64 {
	if s.Threshold == nil || *s.Threshold <= 0 {
		return semcache.DefaultThreshold
	}
	return *s.Threshold
}

// EffectiveTTL 返回条目生存时间；未配置或非正时取 semcache 的默认值。
func (s Semcache) EffectiveTTL() time.Duration {
	if s.TTLSeconds == nil || *s.TTLSeconds <= 0 {
		return semcache.DefaultTTL
	}
	return time.Duration(*s.TTLSeconds) * time.Second
}

// EffectiveMaxEntries 返回容量上限；未配置或非正时取 semcache 的默认值。
func (s Semcache) EffectiveMaxEntries() int {
	if s.MaxEntries == nil || *s.MaxEntries <= 0 {
		return semcache.DefaultMaxEntries
	}
	return *s.MaxEntries
}

// EffectiveBanDuration 返回临时封禁时长；未配置或非正时取默认 60s。
func (m Moderation) EffectiveBanDuration() time.Duration {
	return m.AntiSpam.BanDuration.Or(defaultModerationBanDuration)
}

// EffectiveDuplicateRepeat 返回连续重复消息阈值；未配置或非正时取默认 5。
func (m Moderation) EffectiveDuplicateRepeat() int {
	return orPositive(m.AntiSpam.DuplicateRepeat, defaultModerationDuplicateRepeat)
}
