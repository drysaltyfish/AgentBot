package config

import (
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Problem 是一条配置问题。
type Problem struct {
	Path string
	Msg  string
}

// ValidationError 汇总全部配置问题（不是遇到第一个就停）。
type ValidationError struct {
	Problems []Problem
}

// Error 实现 error。
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		parts = append(parts, p.Path+": "+p.Msg)
	}
	return "config validation failed:\n  - " + strings.Join(parts, "\n  - ")
}

// Unwrap 让 errors.Is(err, ErrInvalid) 成立。
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Has 判断某个路径是否出错（测试用）。
func (e *ValidationError) Has(path string) bool {
	for _, p := range e.Problems {
		if p.Path == path {
			return true
		}
	}
	return false
}

// Validate 收集全部问题后一次性返回；没有问题时返回 nil。
func (c *Config) Validate() error {
	var problems []Problem
	add := func(path, msg string) { problems = append(problems, Problem{Path: path, Msg: msg}) }

	switch c.Transport.Mode {
	case "wsclient":
		if c.Transport.URL == "" {
			add("transport.url", "mode=wsclient 时必须提供上报地址")
		} else if !strings.HasPrefix(c.Transport.URL, "ws://") && !strings.HasPrefix(c.Transport.URL, "wss://") {
			add("transport.url", "必须是 ws:// 或 wss:// 开头")
		}
	case "wsserver", "http":
		if c.Transport.AccessToken == nil || *c.Transport.AccessToken == "" {
			add("transport.access_token", "mode="+c.Transport.Mode+" 是入站入口，必须配置 access_token（fail-closed，见 F-80）")
		}
	default:
		add("transport.mode", "必须是 wsclient / wsserver / http 之一，实际为 "+strconv.Quote(c.Transport.Mode))
	}
	if c.Transport.Backoff != nil && c.Transport.Backoff.D <= 0 {
		add("transport.backoff", "必须为正")
	}

	if c.LLM.Provider == "" {
		add("llm.provider", "必填")
	}
	if c.LLM.Model == "" {
		add("llm.model", "必填")
	}
	if c.LLM.APIKey != nil && *c.LLM.APIKey == "" {
		add("llm.api_key", "显式配置为空；若该 provider 启用则必须提供密钥")
	}
	if c.LLM.Timeout != nil && c.LLM.Timeout.D <= 0 {
		add("llm.timeout", "必须为正")
	}
	if c.LLM.MaxIterations != nil && *c.LLM.MaxIterations <= 0 {
		add("llm.max_iterations", "必须为正")
	}
	switch c.LLM.ReasoningEffort {
	case "", "low", "high", "max":
	default:
		add("llm.reasoning_effort", "必须是 low / high / max 之一，实际为 "+strconv.Quote(c.LLM.ReasoningEffort))
	}
	if c.LLM.HistoryTurns != nil && *c.LLM.HistoryTurns < 0 {
		add("llm.history_turns", "不能为负")
	}
	if c.LLM.APIKeyFile != nil && strings.TrimSpace(*c.LLM.APIKeyFile) != "" {
		b, ferr := os.ReadFile(*c.LLM.APIKeyFile)
		switch {
		case ferr != nil:
			add("llm.api_key_file", "无法读取: "+ferr.Error())
		case strings.TrimSpace(string(b)) == "":
			add("llm.api_key_file", "文件内容为空")
		}
	}
	if c.LLM.SystemPromptFile != nil && strings.TrimSpace(*c.LLM.SystemPromptFile) != "" {
		if _, err := os.Stat(*c.LLM.SystemPromptFile); err != nil {
			add("llm.system_prompt_file", "无法读取: "+err.Error())
		}
	}

	switch c.Behavior.Private {
	case "", ReplyAlways, ReplyNever:
	default:
		add("behavior.private", "必须是 always / never 之一，实际为 "+strconv.Quote(c.Behavior.Private))
	}
	switch c.Behavior.Group {
	case "", ReplyAlways, ReplyNever, ReplyOnMention:
	default:
		add("behavior.group", "必须是 always / on_mention / never 之一，实际为 "+strconv.Quote(c.Behavior.Group))
	}
	switch c.Agent.Protocol {
	case "", "native", "auto":
	default:
		add("agent.protocol", "必须是 native / auto 之一，实际为 "+strconv.Quote(c.Agent.Protocol))
	}
	if c.Agent.MaxIterations != nil && *c.Agent.MaxIterations <= 0 {
		add("agent.max_iterations", "必须为正")
	}
	if c.Agent.StepTimeout != nil && c.Agent.StepTimeout.D <= 0 {
		add("agent.step_timeout", "必须为正")
	}
	if c.Agent.ApprovalTimeout != nil && c.Agent.ApprovalTimeout.D <= 0 {
		add("agent.approval_timeout", "必须为正")
	}
	if c.Store.BusyTimeout != nil && c.Store.BusyTimeout.D <= 0 {
		add("store.busy_timeout", "必须为正")
	}
	if c.LLM.AmbientMaxChars != nil && *c.LLM.AmbientMaxChars < 1 {
		add("llm.ambient_max_chars", "必须 >= 1（负数表示不截断）")
	}
	if c.History.Retention != nil && *c.History.Retention < 1 {
		add("history.retention", "必须 >= 1")
	}
	if c.Agent.MemoryMax != nil && *c.Agent.MemoryMax < 1 {
		add("agent.memory_max", "必须 >= 1")
	}
	if c.Agent.ApprovalEnabled && len(c.Agent.Allow) == 0 {
		add("agent.allow", "启用审批但没有放行表时，所有工具都会等待审批；请至少列出只读工具")
	}
	if c.Behavior.SplitDelay != nil {
		if c.Behavior.SplitDelay.D < 0 {
			add("behavior.split_delay", "不能为负")
		} else if c.Behavior.SplitDelay.D > 10*time.Second {
			add("behavior.split_delay", "超过 10s；连发间隔过大会让回复显得断续")
		}
	}
	if c.Behavior.MaxSegments != nil && *c.Behavior.MaxSegments < 1 {
		add("behavior.max_segments", "必须 >= 1")
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level", "必须是 debug / info / warn / error 之一")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format", "必须是 json 或 text 之一")
	}
	if c.Log.QueueSize != nil && *c.Log.QueueSize <= 0 {
		add("log.queue_size", "必须为正")
	}
	for name, lvl := range c.Log.Components {
		switch lvl {
		case "debug", "info", "warn", "error":
		default:
			add("log.components."+name, "必须是 debug / info / warn / error 之一")
		}
	}

	if c.Shutdown.Timeout != nil {
		if c.Shutdown.Timeout.D <= 0 {
			add("shutdown.timeout", "必须为正")
		} else if c.Shutdown.Timeout.D > time.Minute {
			add("shutdown.timeout", "超过 1 分钟；关闭必须有界（F-70）")
		}
	}

	if c.RateLimit.UserPerMinute != nil && *c.RateLimit.UserPerMinute < 0 {
		add("ratelimit.user_per_minute", "不能为负")
	}
	if c.RateLimit.UserBurst != nil && *c.RateLimit.UserBurst < 0 {
		add("ratelimit.user_burst", "不能为负")
	}
	if c.RateLimit.GroupPerMinute != nil && *c.RateLimit.GroupPerMinute < 0 {
		add("ratelimit.group_per_minute", "不能为负")
	}
	if c.RateLimit.GroupBurst != nil && *c.RateLimit.GroupBurst < 0 {
		add("ratelimit.group_burst", "不能为负")
	}
	if c.Audit.QueueSize != nil && *c.Audit.QueueSize <= 0 {
		add("audit.queue_size", "必须为正")
	}
	if c.Audit.ContentLimit != nil && *c.Audit.ContentLimit < 1 {
		add("audit.content_limit", "必须 >= 1")
	}
	if c.Ops.ReadyCacheTTL != nil && c.Ops.ReadyCacheTTL.D <= 0 {
		add("ops.ready_cache_ttl", "必须为正")
	}
	if c.Ops.ProbeTimeout != nil && c.Ops.ProbeTimeout.D <= 0 {
		add("ops.probe_timeout", "必须为正")
	}
	switch c.Moderation.Action {
	case "", "mask", "block":
	default:
		add("moderation.action", "必须是 mask / block 之一，实际为 "+strconv.Quote(c.Moderation.Action))
	}
	if c.Moderation.SensitiveWordsFile != "" {
		if _, ferr := os.Stat(c.Moderation.SensitiveWordsFile); ferr != nil {
			add("moderation.sensitive_words_file", "无法读取: "+ferr.Error())
		}
	}
	switch c.Singleflight.Key {
	case "", "user", "user_group":
	default:
		add("singleflight.key", "必须是 user / user_group 之一，实际为 "+strconv.Quote(c.Singleflight.Key))
	}
	if c.Ops.EffectiveEnabled() {
		if _, _, err := net.SplitHostPort(c.Ops.EffectiveAddr()); err != nil {
			add("ops.addr", "必须是 host:port 形式（如 127.0.0.1:9090）: "+err.Error())
		}
	}

	if c.Semcache.Threshold != nil {
		v := *c.Semcache.Threshold
		if v < 0 || v > 1 {
			add("semcache.threshold", "必须在 (0,1] 区间内，实际为 "+strconv.FormatFloat(v, 'g', -1, 64))
		}
	}
	if c.Semcache.TTLSeconds != nil && *c.Semcache.TTLSeconds < 1 {
		add("semcache.ttl_seconds", "必须 >= 1")
	}
	if c.Semcache.MaxEntries != nil && *c.Semcache.MaxEntries < 1 {
		add("semcache.max_entries", "必须 >= 1")
	}

	// F-58 运维扩展：名单模式与角色指定。
	if c.Access.Enabled != nil && *c.Access.Enabled {
		switch strings.ToLower(strings.TrimSpace(c.Access.Mode)) {
		case "allow", "deny":
		case "":
			add("access.mode", "启用名单时必须显式写 allow 或 deny（不写会静默变成不限制）")
		default:
			add("access.mode", "必须是 allow / deny 之一，实际为 "+strconv.Quote(c.Access.Mode))
		}
		// allow 模式且两个名单都为空 = 把所有人挡在门外，这几乎总是配置事故。
		if strings.EqualFold(strings.TrimSpace(c.Access.Mode), "allow") &&
			len(c.Access.Users) == 0 && len(c.Access.Groups) == 0 {
			add("access.users", "allow 模式下用户名单与群名单不能同时为空（否则没有任何消息会被处理）")
		}
	}
	for role := range c.Access.Roles {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "superuser", "owner", "admin", "member":
		default:
			add("access.roles", "角色名必须是 superuser / owner / admin / member 之一，实际为 "+strconv.Quote(role))
		}
	}
	if c.Access.BypassSuperUsers != nil && !*c.Access.BypassSuperUsers && c.Access.EffectiveEnabled() {
		hasSuper := len(c.Moderation.SuperUsers) > 0
		for role, ids := range c.Access.Roles {
			if strings.EqualFold(strings.TrimSpace(role), "superuser") && len(ids) > 0 {
				hasSuper = true
			}
		}
		if !hasSuper {
			add("access.bypass_super_users", "关闭超管绕过，却又没有配置任何超管：一旦名单配错将无人能管理")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return &ValidationError{Problems: problems}
}
