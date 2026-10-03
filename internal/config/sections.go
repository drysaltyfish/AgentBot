package config

// Store 描述持久层（F-83）。
type Store struct {
	// Path 是数据库文件路径；为空时用 store.DefaultPath()。
	Path string `yaml:"path"`
	// BusyTimeout <= 0 时用 store.DefaultBusyTimeout（1s）。
	BusyTimeout *Duration `yaml:"busy_timeout"`
}

// History 描述对话历史的存储（F-38 的落盘选项）。
type History struct {
	// File 是 JSONL 落盘路径；为空表示仅进程内（重启即丢）。
	File string `yaml:"file"`
	// Retention 是**存储**保留的条目上限（默认 400），远大于呈现窗口。
	// 呈现窗口由 llm.history_turns 决定；两者分开，recall_history 才能召回
	// 窗口之外的旧内容。
	Retention *int `yaml:"retention"`
}

// Agent 描述 ReAct 循环与工具系统的接入方式（F-35 / F-41 / F-44 / F-45）。
type Agent struct {
	// Paradigm 是 Agent 范式：空/react（默认，基础实现）、reflexion、orchestrator。
	Paradigm *string `yaml:"paradigm"`
	// Reflexion 是反思范式的参数（F-36）。
	Reflexion Reflexion `yaml:"reflexion"`
	// Enabled 为 true 时回复链路走 ReAct 循环（带工具）；为 false 时直连 LLM。
	Enabled bool `yaml:"enabled"`
	// MaxIterations <= 0 时用 agent.DefaultMaxIterations。
	MaxIterations *int `yaml:"max_iterations"`
	// StepTimeout <= 0 时用 agent.DefaultStepTimeout。
	StepTimeout *Duration `yaml:"step_timeout"`
	// Protocol 为 native 或 auto（默认 auto）。
	Protocol string `yaml:"protocol"`
	// Tools 是要注册的内置工具名；为空表示全部注册。
	Tools []string `yaml:"tools"`
	// VirtualActions 为 true 时注册 end_action / save_memory / noop。
	VirtualActions *bool `yaml:"virtual_actions"`
	// Memory 为 true 时启用进程内长期记忆（F-48 的完整实现在 M3）。
	Memory *bool `yaml:"memory"`
	// MemoryMax 是记忆条数上限（每个作用域），默认 64。
	MemoryMax *int `yaml:"memory_max"`
	// MemoryFile 是记忆落盘的 JSONL 路径；为空表示仅进程内（重启即丢）。
	// 复用 F-38 的历史存储实现（F-47 的“可选 JSONL 文件落盘”）。
	MemoryFile string `yaml:"memory_file"`
	// ApprovalTimeout 是人工审批的独立预算，默认 60s。
	ApprovalTimeout *Duration `yaml:"approval_timeout"`
	// ApprovalEnabled 为 true 时启用 F-45 权限闸门。
	// 默认关闭：空权限表是 fail-closed 的（所有工具都需要审批），
	// 没配审批通道就打开会导致工具全部不可用。
	ApprovalEnabled bool `yaml:"approval_enabled"`
	// Allow 是「工具 -> 角色」的放行表，仅在 ApprovalEnabled 时生效。
	// 形如 {calculator: [member], json_query: [member]}：列出的角色可直接执行。
	Allow map[string][]string `yaml:"allow"`
	// AutoMemory 是显式记忆指令的自动写入（F-48 的规则触发）。
	AutoMemory AutoMemory `yaml:"auto_memory"`
	// ProactiveMemory 让模型自己判断什么值得长期记住。
	ProactiveMemory ProactiveMemory `yaml:"proactive_memory"`
	// MemoryJudge 控制记忆写入的语义判官（F-87）。
	MemoryJudge MemoryJudge `yaml:"memory_judge"`
	// ToolHint 是提示模型如何使用工具（例如用 recall_history 回溯引用内容）。
	ToolHint ToolHint `yaml:"tool_hint"`
}

// ToolHint 描述工具使用提示。
type ToolHint struct {
	// Enabled 为 nil 时按启用处理。
	Enabled *bool `yaml:"enabled"`
	// Instruction 覆盖内置提示；为空时使用默认。
	Instruction string `yaml:"instruction"`
}

// MemoryJudge 描述记忆写入的语义判官。
//
// 判官只在**相似度落在歧义带**时被问一次：字符相似度足以处理明显的情况，
// 只有真正含糊的那一小撮（例如「旧的一条」与「新的一条」相似度正好 0.50）
// 才值得花一次模型调用。判官不可用时退回确定性判据，写入照常成功。
type MemoryJudge struct {
	// Enabled 为 nil 时按启用处理。
	Enabled *bool `yaml:"enabled"`
}

// AutoMemory 描述"记住：xxx"这类指令的自动写入。
//
// 只做**规则触发**：确定性、不额外调用 LLM。与 ProactiveMemory 是两条独立通道，
// 可任选其一或同时开启。
type AutoMemory struct {
	// Enabled 为 true 时启用。**默认关闭**。
	Enabled bool `yaml:"enabled"`
	// Triggers 覆盖默认触发词；为空时使用 agent.DefaultMemoryTriggers。
	Triggers []string `yaml:"triggers"`
}

// ProactiveMemory 通过系统提示词要求模型主动保存值得记住的事实。
//
// 与规则触发的区别：这是**概率性**的（模型自行判断），可能漏记也可能误记；
// 好处是不依赖用户说"记住"，能从自由对话里捕捉重要信息。
type ProactiveMemory struct {
	// Enabled 为 nil 时按启用处理（这是默认的记忆写入通道）。
	Enabled *bool `yaml:"enabled"`
	// Instruction 覆盖内置指令；为空时使用 agent.DefaultProactiveMemoryInstruction。
	Instruction string `yaml:"instruction"`
}

// Behavior 描述回复行为（F-13 路由策略的配置面）。
type Behavior struct {
	// Private 是私聊策略：always / never。
	Private string `yaml:"private"`
	// Group 是群聊策略：always / on_mention / never。
	Group string `yaml:"group"`

	// SplitOnBlankLine 为 true 时，回复里的空行会被拆成多条消息分别发送。
	// 聊天窗口里一大块文字读起来很累，真人是一条一条发的。默认 true。
	SplitOnBlankLine *bool `yaml:"split_on_blank_line"`
	// SplitDelay 是连发之间的间隔，默认 400ms。
	SplitDelay *Duration `yaml:"split_delay"`
	// MaxSegments 是单次回复最多拆成几条，默认 4；超出部分合并进最后一条。
	MaxSegments *int `yaml:"max_segments"`
}

// Transport 描述事件接入方式与入站鉴权（F-04 / F-80）。
type Transport struct {
	Mode            string    `yaml:"mode"`
	URL             string    `yaml:"url"`
	AccessToken     *string   `yaml:"access_token"`
	SignatureSecret *string   `yaml:"signature_secret"`
	IPAllowlist     []string  `yaml:"ip_allowlist"`
	SelfID          *int64    `yaml:"self_id"`
	Backoff         *Duration `yaml:"backoff"`
}

// LLM 描述模型供应商接入（F-26）。
// Pricing 是模型单价（每百万 token 的美元价）。全 0 表示未配置，成本不参与统计。
type Pricing struct {
	Version            string   `yaml:"version"`
	InputPerMillion    *float64 `yaml:"input_per_million"`
	OutputPerMillion   *float64 `yaml:"output_per_million"`
	CacheHitPerMillion *float64 `yaml:"cache_hit_per_million"`
}

type LLM struct {
	Provider string  `yaml:"provider"`
	Model    string  `yaml:"model"`
	BaseURL  string  `yaml:"base_url"`
	APIKey   *string `yaml:"api_key"`
	// APIKeyEnv 是优先读取的环境变量名（F-61 的第一优先级）。
	APIKeyEnv *string `yaml:"api_key_env"`
	// APIKeyFile 是密钥文件路径（第二优先级）；Unix 下要求 0600，不合规只告警。
	APIKeyFile    *string   `yaml:"api_key_file"`
	Timeout       *Duration `yaml:"timeout"`
	MaxIterations *int      `yaml:"max_iterations"`

	// Thinking 控制思考模式；nil 表示不下发该字段。
	Thinking *bool `yaml:"thinking"`
	// ReasoningEffort 是思考强度：low / high / max。
	ReasoningEffort string `yaml:"reasoning_effort"`

	// SystemPrompt 是不可变前缀的正文（缓存优先的关键：每个会话逐字节相同）。
	// 为空时使用内置默认。
	SystemPrompt *string `yaml:"system_prompt"`
	// SystemPromptFile 从文件读取不可变前缀正文，优先级高于 SystemPrompt。
	// 长人格提示词放文件更易维护；启动时读一次并固定，保证前缀逐字节稳定。
	SystemPromptFile *string `yaml:"system_prompt_file"`
	// HistoryTurns 是最多回灌多少条历史；<=0 或未设置时用默认值。
	HistoryTurns *int `yaml:"history_turns"`
	// AmbientTokenBudget 是环境消息（群里没被 @ 的）的 token 预算。
	// 0 用默认值；负数表示不压缩。
	AmbientTokenBudget *int `yaml:"ambient_token_budget"`
	// AmbientMaxChars 是单条环境消息的字符上限，超出截断并提示可回溯。
	AmbientMaxChars *int `yaml:"ambient_max_chars"`
	Pricing         Pricing
}

// Prompt 描述提示词资产位置（F-33 / F-82）。
type Prompt struct {
	Dir     string  `yaml:"dir"`
	Persona *string `yaml:"persona"`
	// PersonasDir 是人格定义目录（F-82）；为空时取 Dir 下的 personas 子目录。
	PersonasDir *string `yaml:"personas_dir"`
}

// Policy 描述权限表位置（F-53）。
type Policy struct {
	File string `yaml:"file"`
}

// Log 描述日志行为（F-67）。
type Log struct {
	Level        string            `yaml:"level"`
	Format       string            `yaml:"format"`
	Components   map[string]string `yaml:"components"`
	DebugContent bool              `yaml:"debug_content"`
	QueueSize    *int              `yaml:"queue_size"`
}

// Shutdown 描述优雅关闭（F-70）。
type Shutdown struct {
	Timeout *Duration `yaml:"timeout"`
}

// RateLimit 描述令牌桶限速（F-18）。
//
// 限速会改变行为（超限事件被整条丢弃），因此**默认关闭**，需要显式打开。
type RateLimit struct {
	// Enabled 为 nil 时按关闭处理。
	Enabled *bool `yaml:"enabled"`
	// UserPerMinute 是单用户每分钟可用次数；未配置或非正时取默认 20。
	UserPerMinute *int `yaml:"user_per_minute"`
	// UserBurst 是单用户的突发容量（桶容量）；未配置或非正时取默认 5。
	UserBurst *int `yaml:"user_burst"`
	// GroupPerMinute 是单群每分钟可用次数；未配置或非正时取默认 120。
	GroupPerMinute *int `yaml:"group_per_minute"`
	// GroupBurst 是单群的突发容量；未配置或非正时取默认 20。
	GroupBurst *int `yaml:"group_burst"`
}

// Toggle 描述功能开关（F-19）：群管可以按群关闭某个插件，且重启后保持。
type Toggle struct {
	// Enabled 为 nil 时按关闭处理。
	Enabled *bool `yaml:"enabled"`
	// DefaultOn 是未显式设置过的 (plugin, group) 的默认状态；nil 时按 true 处理。
	DefaultOn *bool `yaml:"default_on"`
	// File 是开关状态的落盘路径；为空表示仅进程内（重启即丢）。
	File string `yaml:"file"`
}

// Audit 描述审计日志（F-60）。
//
// 审计**不可关闭**（F-60 明确要求），只可调去处与内容保留量，因此没有 enabled 字段。
type Audit struct {
	// File 是审计 JSONL 的落盘路径；为空时只写 stdout。
	File string `yaml:"file"`
	// Stdout 为 true 时同时写标准输出（便于采集器直接抓）。
	Stdout bool `yaml:"stdout"`
	// QueueSize 是异步队列容量；未配置或非正时取默认 4096。
	QueueSize *int `yaml:"queue_size"`
	// ContentLimit 是用户内容保留的字符数；未配置或小于 1 时取默认 20。
	ContentLimit *int `yaml:"content_limit"`
}

// Singleflight 描述单飞（反并发）中间件（F-17）。
//
// 默认关闭：它会让"同一 key 的第二次并发请求"被拒绝，属于改变行为的开关。
type Singleflight struct {
	// Enabled 为 nil 时按关闭处理。
	Enabled *bool `yaml:"enabled"`
	// Key 是单飞的粒度：user（按用户）或 user_group（按用户+群，默认）。
	Key string `yaml:"key"`
	// Notice 为 true 时对被拒绝的请求回一句提示；默认 false，避免刷屏。
	Notice *bool `yaml:"notice"`
}

// Cost 描述成本统计与配额（F-66）。默认关闭。
type Cost struct {
	Enabled *bool `yaml:"enabled"`
	// UnknownModel 是未识别模型的计价策略：warn_zero（默认）或 reject。
	UnknownModel string `yaml:"unknown_model"`
	// QueueSize 是异步持久化队列长度；<=0 取 256。
	QueueSize *int `yaml:"queue_size"`
	// Prices 是"每模型每千 token 单价"表。
	Prices map[string]CostPrice `yaml:"prices"`
	// Quotas 是配额列表。
	Quotas []CostQuota `yaml:"quotas"`
}

// CostPrice 是单个模型的价格（每千 token）。
type CostPrice struct {
	InputPer1K  float64 `yaml:"input_per_1k"`
	OutputPer1K float64 `yaml:"output_per_1k"`
}

// CostQuota 是一条配额。
type CostQuota struct {
	Scope          string  `yaml:"scope"`  // global | session | user
	Period         string  `yaml:"period"` // day | month | total
	Limit          float64 `yaml:"limit"`
	SoftLimit      float64 `yaml:"soft_limit"`
	Action         string  `yaml:"action"` // deny | downgrade | warn
	DowngradeModel string  `yaml:"downgrade_model"`
}

// EffectiveEnabled 返回是否启用成本统计；未配置时默认关闭。
func (c Cost) EffectiveEnabled() bool { return orBool(c.Enabled, false) }

// EffectiveQueueSize 返回异步持久化队列长度；未配置或非正时取 256。
func (c Cost) EffectiveQueueSize() int {
	if c.QueueSize == nil || *c.QueueSize <= 0 {
		return 256
	}
	return *c.QueueSize
}

// EffectiveUnknownModel 返回未识别模型的计价策略；未配置时默认 warn_zero。
func (c Cost) EffectiveUnknownModel() string {
	if c.UnknownModel == "" {
		return "warn_zero"
	}
	return c.UnknownModel
}

// Sandbox 描述工具执行沙箱（F-46）。
//
// 默认关闭：它会限制既有工具的能力，属于改变行为的开关。
type Sandbox struct {
	Enabled *bool `yaml:"enabled"`
	// MaxOutputBytes 是单次工具输出的字节上限；<=0 取 64KiB。
	MaxOutputBytes *int `yaml:"max_output_bytes"`
	// ReadRoots / WriteRoots 是可访问的路径白名单根。
	ReadRoots  []string `yaml:"read_roots"`
	WriteRoots []string `yaml:"write_roots"`
	// EnvAllowlist 是允许透传的环境变量名。
	EnvAllowlist []string `yaml:"env_allowlist"`
	// ForbiddenOps / ForbiddenTools 是禁止的操作与工具。
	ForbiddenOps   []string `yaml:"forbidden_ops"`
	ForbiddenTools []string `yaml:"forbidden_tools"`
	// NetworkTools 是允许联网的工具名（其余工具默认禁网）。
	NetworkTools    []string `yaml:"network_tools"`
	AllowNetwork    *bool    `yaml:"allow_network"`
	RequireReadOnly *bool    `yaml:"require_read_only"`
}

// EffectiveEnabled 返回是否启用工具沙箱；未配置时默认关闭。
func (s Sandbox) EffectiveEnabled() bool { return orBool(s.Enabled, false) }

// EffectiveMaxOutputBytes 返回输出上限；未配置或非正时取 64KiB。
func (s Sandbox) EffectiveMaxOutputBytes() int {
	if s.MaxOutputBytes == nil || *s.MaxOutputBytes <= 0 {
		return 64 << 10
	}
	return *s.MaxOutputBytes
}

// EffectiveAllowNetwork 返回是否默认允许联网；未配置时默认禁止。
func (s Sandbox) EffectiveAllowNetwork() bool { return orBool(s.AllowNetwork, false) }

// EffectiveRequireReadOnly 返回是否要求工具只读；未配置时默认不要求。
func (s Sandbox) EffectiveRequireReadOnly() bool { return orBool(s.RequireReadOnly, false) }

// Moderation 描述入站内容审查与黑名单（F-57 / F-58）。
//
// 默认关闭：它会改写或拦截入站消息，属于改变行为的开关。
type Moderation struct {
	// Enabled 为 nil 时按关闭处理。
	Enabled *bool `yaml:"enabled"`
	// SensitiveWords 是敏感词表（内联）；与文件叠加。
	SensitiveWords []string `yaml:"sensitive_words"`
	// SensitiveWordsFile 是敏感词表文件（每行一个词，忽略空行与 # 注释）。
	SensitiveWordsFile string `yaml:"sensitive_words_file"`
	// Action 是命中后的动作：mask（脱敏放行，默认）或 block（拦截）。
	Action string `yaml:"action"`
	// MaskReplacement 是脱敏替换串；为空时按命中长度生成等长掩码。
	MaskReplacement *string `yaml:"mask_replacement"`
	// BlacklistFile 是封禁记录的落盘路径；为空表示仅进程内（重启即丢）。
	BlacklistFile string `yaml:"blacklist_file"`
	// SuperUsers 是永不封禁的超管用户 ID。
	SuperUsers []int64 `yaml:"super_users"`
	// AntiSpam 是防刷参数（F-58）。
	AntiSpam ModerationAntiSpam `yaml:"antispam"`
}

// ModerationAntiSpam 描述防刷参数（F-58）。
type ModerationAntiSpam struct {
	// Window 是速率统计窗口；未配置或非正时取默认 10s。
	Window *Duration `yaml:"window"`
	// MaxMessages 是窗口内允许的最大消息数；未配置或非正时取默认 20。
	MaxMessages *int `yaml:"max_messages"`
	// BanDuration 是触发后的临时封禁时长；未配置或非正时取默认 60s。
	BanDuration *Duration `yaml:"ban_duration"`
	// DuplicateRepeat 是连续相同消息的阈值；未配置或非正时取默认 5。
	DuplicateRepeat *int `yaml:"duplicate_repeat"`
}

// Ops 描述指标与探针的独立监听（F-68 / F-69）。
//
// 默认只监听回环地址：指标与探针不需要对外暴露，这也是默认不配鉴权也安全的前提。
type Ops struct {
	// Enabled 为 nil 时按启用处理。
	Enabled *bool `yaml:"enabled"`
	// Addr 是监听地址；为空时取默认 127.0.0.1:9090。
	Addr string `yaml:"addr"`
	// AuthToken 非空时三个端点都要求 Authorization: Bearer <token>。
	AuthToken *string `yaml:"auth_token"`
	// ReadyCacheTTL 是就绪检查结果的缓存时长；未配置或非正时取默认 10s。
	ReadyCacheTTL *Duration `yaml:"ready_cache_ttl"`
	// ProbeTimeout 是单次依赖检查的超时；未配置或非正时取默认 1s。
	ProbeTimeout *Duration `yaml:"probe_timeout"`
}

// Reflexion 是反思范式参数（F-36）。
type Reflexion struct {
	// MaxReflections 是最大反思轮数；未配置或非正时取 1。
	MaxReflections *int `yaml:"max_reflections"`
	// Threshold 是"够好就停"的分数阈值；未配置或非正时取 1.0。
	Threshold *float64 `yaml:"threshold"`
}

// EffectiveParadigm 返回 Agent 范式；未配置时为空串（表示用基础实现）。
func (a Agent) EffectiveParadigm() string {
	if a.Paradigm == nil {
		return ""
	}
	return *a.Paradigm
}

// EffectiveMaxReflections 返回最大反思轮数；未配置或非正时取 1。
func (a Agent) EffectiveMaxReflections() int {
	if a.Reflexion.MaxReflections == nil || *a.Reflexion.MaxReflections <= 0 {
		return 1
	}
	return *a.Reflexion.MaxReflections
}

// EffectiveThreshold 返回反思停止阈值；未配置或非正时取 1.0。
func (a Agent) EffectiveThreshold() float64 {
	if a.Reflexion.Threshold == nil || *a.Reflexion.Threshold <= 0 {
		return 1.0
	}
	return *a.Reflexion.Threshold
}
