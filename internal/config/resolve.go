package config

import (
	"time"

	"github.com/drysaltyfish/agentbot/internal/conversation"
)

// Effective 返回一份“生效值”副本：凡是带默认值的字段都填成运行时真正会用的值。
//
// 存在的理由：默认值有**两套机制**——defaults.go 在装载时填一部分字段，
// effective.go 的 accessor 在读取时兜底另一部分。于是 --check-config 打印
// 原始配置时，靠 accessor 兜底的字段显示成 null，而运维看到的数字与真正跑的
// 不是同一个（历史上就因此把 history_turns: 0 显示成 0、实际跑的却是 20）。
//
// 规则：
//   - 只对**指针字段**赋值，字符串字段的派生值（如 ops.addr）不动；
//   - 值取自对应的 accessor，所以“打印出来的”与“运行时读到的”必然同源；
//   - 显式配置的值通常原样保留（accessor 对显式值就是原样返回）；
//   - 有一类字段刻意**保持 nil**，因为“没有”本身就是它的配置：
//     llm.api_key / transport.access_token / transport.signature_secret（密钥）、
//     llm.system_prompt_file（回退内联提示词）、prompt.persona（回退内置人格）、
//     agent.paradigm（回退基础实现）、llm.thinking（nil = 不下发该字段，是三态而非默认值）。
//
// 返回值是新副本，绝不改动接收者。
func (c *Config) Effective() *Config {
	if c == nil {
		return nil
	}
	r := *c

	// --- transport ---
	// self_id 未配置时按 0 处理（表示"以平台上报为准"），显式 0 亦然。
	resolve(&r.Transport.SelfID, c.Transport.EffectiveSelfID())

	// --- access：名单与角色 ---
	resolve(&r.Access.Enabled, c.Access.EffectiveEnabled())
	resolve(&r.Access.CheckUsersInGroup, c.Access.EffectiveCheckUsersInGroup())
	resolve(&r.Access.BypassSuperUsers, c.Access.EffectiveBypassSuperUsers())
	resolve(&r.Access.LogDrops, c.Access.EffectiveLogDrops())

	// --- llm：模型接入与上下文预算 ---
	resolve(&r.LLM.HistoryTurns, c.LLM.EffectiveHistoryTurns())
	resolve(&r.LLM.MaxContext, c.LLM.EffectiveMaxContext())
	resolve(&r.LLM.ReserveOutput, c.LLM.EffectiveReserveOutput())
	resolve(&r.LLM.ReserveTools, c.LLM.EffectiveReserveTools())
	// 环境消息预算的默认值由消费方 internal/conversation 拥有，这里引用同一份常量，
	// 避免默认值出现第三个副本。
	resolve(&r.LLM.AmbientTokenBudget, c.LLM.AmbientTokenBudgetOr(conversation.DefaultAmbientTokenBudget))
	resolve(&r.LLM.AmbientMaxChars, c.LLM.AmbientMaxCharsOr(conversation.DefaultAmbientMaxChars))

	// --- history / store / shutdown ---
	resolve(&r.History.Retention, c.History.EffectiveRetention())
	resolve(&r.Shutdown.Timeout, Duration{D: c.Shutdown.EffectiveTimeout()})

	// --- agent：循环、记忆与反思 ---
	resolve(&r.Agent.Memory, c.Agent.EffectiveMemory())
	resolve(&r.Agent.VirtualActions, c.Agent.EffectiveVirtualActions())
	resolve(&r.Agent.MemoryMax, c.Agent.EffectiveMemoryMax())
	resolve(&r.Agent.Reflexion.MaxReflections, c.Agent.EffectiveMaxReflections())
	resolve(&r.Agent.Reflexion.Threshold, c.Agent.EffectiveThreshold())
	// 注意：auto_memory.enabled 是普通 bool（不是指针），没有“未设置”态，故不参与解析。
	resolve(&r.Agent.ProactiveMemory.Enabled, c.Agent.ProactiveMemory.EffectiveEnabled())
	resolve(&r.Agent.MemoryJudge.Enabled, c.Agent.MemoryJudge.EffectiveEnabled())
	resolve(&r.Agent.ToolHint.Enabled, c.Agent.ToolHint.EffectiveEnabled())
	resolve(&r.Agent.Reflect.Enabled, c.Agent.Reflect.EffectiveEnabled())
	resolve(&r.Agent.Reflect.Every, Duration{D: c.Agent.Reflect.EffectiveEvery()})
	resolve(&r.Agent.Reflect.IdleAfter, Duration{D: c.Agent.Reflect.EffectiveIdleAfter()})
	resolve(&r.Agent.Reflect.MinInterval, Duration{D: c.Agent.Reflect.EffectiveMinInterval()})
	resolve(&r.Agent.Reflect.MaxItems, c.Agent.Reflect.EffectiveMaxItems())
	resolve(&r.Agent.Reflect.MaxFacts, c.Agent.Reflect.EffectiveMaxFacts())
	resolve(&r.Agent.Reflect.DailyBudget, c.Agent.Reflect.EffectiveDailyBudget())
	resolve(&r.Agent.Reflect.Timeout, Duration{D: c.Agent.Reflect.EffectiveTimeout()})

	// --- behavior ---
	resolve(&r.Behavior.SplitOnBlankLine, c.Behavior.EffectiveSplitOnBlankLine())
	resolve(&r.Behavior.SplitDelay, Duration{D: c.Behavior.EffectiveSplitDelay()})
	resolve(&r.Behavior.MaxSegments, c.Behavior.EffectiveMaxSegments())

	// --- prompt ---
	resolve(&r.Prompt.PersonasDir, c.Prompt.EffectivePersonasDir())

	// --- log / audit / ops ---
	resolve(&r.Log.QueueSize, c.Log.EffectiveQueueSize())
	resolve(&r.Audit.QueueSize, c.Audit.EffectiveQueueSize())
	resolve(&r.Audit.ContentLimit, c.Audit.EffectiveContentLimit())
	resolve(&r.Ops.Enabled, c.Ops.EffectiveEnabled())
	resolve(&r.Ops.ReadyCacheTTL, Duration{D: c.Ops.EffectiveReadyCacheTTL()})
	resolve(&r.Ops.ProbeTimeout, Duration{D: c.Ops.EffectiveProbeTimeout()})

	// --- ratelimit / toggle / singleflight ---
	resolve(&r.RateLimit.Enabled, c.RateLimit.EffectiveEnabled())
	resolve(&r.RateLimit.UserPerMinute, c.RateLimit.EffectiveUserPerMinute())
	resolve(&r.RateLimit.UserBurst, c.RateLimit.EffectiveUserBurst())
	resolve(&r.RateLimit.GroupPerMinute, c.RateLimit.EffectiveGroupPerMinute())
	resolve(&r.RateLimit.GroupBurst, c.RateLimit.EffectiveGroupBurst())
	resolve(&r.Toggle.Enabled, c.Toggle.EffectiveEnabled())
	resolve(&r.Toggle.DefaultOn, c.Toggle.EffectiveDefaultOn())
	resolve(&r.Singleflight.Enabled, c.Singleflight.EffectiveEnabled())
	resolve(&r.Singleflight.Notice, c.Singleflight.EffectiveNotice())

	// --- moderation ---
	resolve(&r.Moderation.Enabled, c.Moderation.EffectiveEnabled())
	resolve(&r.Moderation.AntiSpam.Window, Duration{D: c.Moderation.EffectiveSpamWindow()})
	resolve(&r.Moderation.AntiSpam.MaxMessages, c.Moderation.EffectiveSpamMaxMessages())
	resolve(&r.Moderation.AntiSpam.BanDuration, Duration{D: c.Moderation.EffectiveBanDuration()})
	resolve(&r.Moderation.AntiSpam.DuplicateRepeat, c.Moderation.EffectiveDuplicateRepeat())

	// --- sandbox / cost / semcache / stream ---
	resolve(&r.Sandbox.Enabled, c.Sandbox.EffectiveEnabled())
	resolve(&r.Sandbox.MaxOutputBytes, c.Sandbox.EffectiveMaxOutputBytes())
	resolve(&r.Sandbox.AllowNetwork, c.Sandbox.EffectiveAllowNetwork())
	resolve(&r.Sandbox.RequireReadOnly, c.Sandbox.EffectiveRequireReadOnly())
	resolve(&r.Cost.Enabled, c.Cost.EffectiveEnabled())
	resolve(&r.Cost.QueueSize, c.Cost.EffectiveQueueSize())
	resolve(&r.Semcache.Enabled, c.Semcache.EffectiveEnabled())
	resolve(&r.Semcache.Threshold, c.Semcache.EffectiveThreshold())
	// ttl_seconds 是秒数，而 EffectiveTTL 返回 Duration（消费方要的形态）——
	// 这里换回秒，保证打印的是运行时会用的那个数字。
	resolve(&r.Semcache.TTLSeconds, int(c.Semcache.EffectiveTTL()/time.Second))
	resolve(&r.Semcache.MaxEntries, c.Semcache.EffectiveMaxEntries())
	resolve(&r.Stream.Enabled, c.Stream.EffectiveEnabled())
	resolve(&r.Stream.MaxChars, c.Stream.EffectiveMaxChars())
	resolve(&r.Stream.MaxInterval, Duration{D: c.Stream.EffectiveMaxInterval()})
	resolve(&r.Stream.MinInterval, Duration{D: c.Stream.EffectiveMinInterval()})
	resolve(&r.Stream.FirstMinChars, c.Stream.EffectiveFirstMinChars())
	resolve(&r.Stream.EditMessages, c.Stream.EffectiveEditMessages())

	// --- retrieval 与摘要树 ---
	resolve(&r.Retrieval.Enabled, c.Retrieval.EffectiveEnabled())
	resolve(&r.Retrieval.KeywordWeight, c.Retrieval.EffectiveKeywordWeight())
	resolve(&r.Retrieval.VectorWeight, c.Retrieval.EffectiveVectorWeight())
	resolve(&r.Retrieval.TopK, c.Retrieval.EffectiveTopK())
	resolve(&r.Retrieval.CandidateK, c.Retrieval.EffectiveCandidateK())
	resolve(&r.Retrieval.Tree.Enabled, c.Retrieval.Tree.EffectiveEnabled())
	resolve(&r.Retrieval.Tree.MaxLevels, c.Retrieval.Tree.EffectiveMaxLevels())
	resolve(&r.Retrieval.Tree.MinCluster, c.Retrieval.Tree.EffectiveMinCluster())
	resolve(&r.Retrieval.Tree.Branching, c.Retrieval.Tree.EffectiveBranching())
	resolve(&r.Retrieval.Tree.MaxNodes, c.Retrieval.Tree.EffectiveMaxNodes())
	resolve(&r.Retrieval.Tree.ClusterThreshold, c.Retrieval.Tree.EffectiveClusterThreshold())

	return &r
}

// resolve 把字段设成它的生效值（accessor 的结果）。
//
// 刻意**无条件赋值**而不是“nil 才填”：accessor 对显式值就是原样返回，
// 所以对显式配置来说这是一次恒等赋值；而对“显式 0 = 未设置”这类字段
// （如 retrieval.keyword_weight），它保证打印出的数字就是运行时用的数字。
func resolve[T any](dst **T, v T) { *dst = &v }
