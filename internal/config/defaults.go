package config

import "time"

// 模块默认值集中在此，Default() 与 effective.go 里的取值访问器共用同一份常量，
// 避免默认值散落在调用方与 Default() 两处后发生漂移。
const (
	// defaultTransportBackoff 是断线重连的默认退避。
	defaultTransportBackoff = time.Second
	// defaultLLMTimeout 是模型请求的默认超时。
	defaultLLMTimeout = 30 * time.Second
	// defaultLLMHistoryTurns 是默认回灌的历史条数。
	defaultLLMHistoryTurns = 20
	// defaultAgentMaxIterations 与 agent.DefaultMaxIterations 对齐。
	defaultAgentMaxIterations = 10
	// defaultAgentStepTimeout 与 agent.DefaultStepTimeout 对齐。
	defaultAgentStepTimeout = 30 * time.Second
	// defaultAgentApprovalTimeout 与 agent.DefaultApprovalTimeout 对齐。
	defaultAgentApprovalTimeout = 60 * time.Second
	// defaultAgentMemoryMax 是每个作用域的默认记忆条数上限。
	defaultAgentMemoryMax = 64
	// defaultLogQueueSize 是日志队列的默认容量。
	defaultLogQueueSize = 1024
	// defaultShutdownTimeout 是优雅关闭的默认预算。
	defaultShutdownTimeout = 10 * time.Second
	// defaultBehaviorSplitDelay 是连发消息之间的默认间隔。
	defaultBehaviorSplitDelay = 400 * time.Millisecond
	// defaultBehaviorMaxSegments 是单次回复默认最多拆成几条。
	defaultBehaviorMaxSegments = 4
	// defaultHistoryRetention 是历史存储默认保留的条目上限。
	defaultHistoryRetention = 400

	// 以下为 F-18 / F-19 / F-60 / F-68 / F-69 的默认值。
	defaultRateLimitUserPerMinute  = 20
	defaultRateLimitUserBurst      = 5
	defaultRateLimitGroupPerMinute = 120
	defaultRateLimitGroupBurst     = 20
	defaultAuditQueueSize          = 4096
	defaultAuditContentLimit       = 20
	// defaultOpsAddr 必须与 internal/ops.DefaultAddr 一致（回环地址）。
	defaultOpsAddr          = "127.0.0.1:9090"
	defaultOpsReadyCacheTTL = 10 * time.Second
	defaultOpsProbeTimeout  = time.Second
	defaultAuditFile        = "./data/audit.jsonl"
	defaultToggleFile       = "./data/toggles.json"
)

// Default 返回带默认值的配置。可选字段用指针，nil 表示“未设置”。
func Default() *Config {
	backoff := Duration{D: defaultTransportBackoff}
	timeout := Duration{D: defaultLLMTimeout}
	queue := defaultLogQueueSize
	sdTimeout := Duration{D: defaultShutdownTimeout}
	historyTurns := defaultLLMHistoryTurns
	maxIterations := defaultAgentMaxIterations
	stepTimeout := Duration{D: defaultAgentStepTimeout}
	approvalTimeout := Duration{D: defaultAgentApprovalTimeout}
	virtualActions := true
	memoryOn := true
	memoryMax := defaultAgentMemoryMax
	splitOnBlank := true
	splitDelay := Duration{D: defaultBehaviorSplitDelay}
	maxSegments := defaultBehaviorMaxSegments
	rlUser, rlUserBurst := defaultRateLimitUserPerMinute, defaultRateLimitUserBurst
	rlGroup, rlGroupBurst := defaultRateLimitGroupPerMinute, defaultRateLimitGroupBurst
	toggleDefaultOn := true
	auditQueue, auditLimit := defaultAuditQueueSize, defaultAuditContentLimit
	opsOn := true
	opsTTL := Duration{D: defaultOpsReadyCacheTTL}
	opsProbe := Duration{D: defaultOpsProbeTimeout}
	return &Config{
		Transport: Transport{Mode: "wsclient", Backoff: &backoff},
		LLM: LLM{
			Provider: "openai", BaseURL: "https://api.openai.com/v1", Timeout: &timeout,
			HistoryTurns: &historyTurns,
		},
		Agent: Agent{
			MaxIterations: &maxIterations, StepTimeout: &stepTimeout,
			Protocol: "auto", VirtualActions: &virtualActions,
			Memory: &memoryOn, MemoryMax: &memoryMax,
			ApprovalTimeout: &approvalTimeout,
		},
		Behavior: Behavior{
			Private: ReplyAlways, Group: ReplyOnMention,
			SplitOnBlankLine: &splitOnBlank, SplitDelay: &splitDelay, MaxSegments: &maxSegments,
		},
		Prompt:   Prompt{Dir: "prompts"},
		Policy:   Policy{File: "actions.yaml"},
		Log:      Log{Level: "info", Format: "json", Components: map[string]string{}, QueueSize: &queue},
		Shutdown: Shutdown{Timeout: &sdTimeout},
		RateLimit: RateLimit{
			UserPerMinute: &rlUser, UserBurst: &rlUserBurst,
			GroupPerMinute: &rlGroup, GroupBurst: &rlGroupBurst,
		},
		Toggle: Toggle{DefaultOn: &toggleDefaultOn, File: defaultToggleFile},
		Audit:  Audit{File: defaultAuditFile, QueueSize: &auditQueue, ContentLimit: &auditLimit},
		Ops:    Ops{Enabled: &opsOn, Addr: defaultOpsAddr, ReadyCacheTTL: &opsTTL, ProbeTimeout: &opsProbe},
	}
}

// Or 把“未设置或配了非正值”解析为 fallback。
//
// 指针为 nil 表示未设置；对于要求为正的字段（超时、退避等），校验层已拒绝 0，
// 因此 <=0 与未设置等价。需要区分“显式 0”的字段（如 behavior.split_delay，
// 0 表示不等待）请用 Behavior.EffectiveSplitDelay，不要用 Or。
func (d *Duration) Or(fallback time.Duration) time.Duration {
	if d == nil || d.D <= 0 {
		return fallback
	}
	return d.D
}
