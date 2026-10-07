package config

// Access 描述「谁的消息会被处理」与「角色的 QQ 号显式指定」（F-58 的运维扩展）。
//
// 与 moderation 的黑名单不同：那里的封禁是**内容处理后**的拦截（要经过审查、防刷），
// 这里是**路由层直接丢弃**——名单外的消息不进入路由匹配、不建会话、不落库、不产生
// 任何模型调用。代价是它只看得到 QQ 号与群号，看不到内容。
type Access struct {
	// Enabled 为 true 时启用名单；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// Mode 是名单模式：off（默认）/ allow（白名单）/ deny（黑名单）。
	Mode string `yaml:"mode"`
	// Users 是名单里的 QQ 号。
	Users []int64 `yaml:"users"`
	// Groups 是名单里的群号。
	Groups []int64 `yaml:"groups"`
	// CheckUsersInGroup 表示用户名单是否也在群里生效；未配置时按 true。
	// allow 模式下为 true 时要求"群在名单 + 人在名单"；
	// deny 模式下为 true 时，群里的用户名单同样会被丢弃。
	CheckUsersInGroup *bool `yaml:"check_users_in_group"`
	// BypassSuperUsers 表示超管是否绕过名单；未配置时按 true。
	// 默认绕过是为了避免"配错白名单把自己锁在门外"——那只能改文件重启才能恢复。
	BypassSuperUsers *bool `yaml:"bypass_super_users"`
	// LogDrops 为 true 时每条被丢弃的消息都记一条日志；未配置时按 false。
	// 无论是否开启，丢弃都会进审计与 events_dropped 指标。
	LogDrops *bool `yaml:"log_drops"`
	// Roles 用 QQ 号直接指定角色，优先于平台上报的群成员角色。
	Roles map[string][]int64 `yaml:"roles"`
}

// Store 描述持久层（F-83）。
type Store struct {
	// Path 是数据库文件路径；为空时用 store.DefaultPath()。
	Path string `yaml:"path"`
	// BusyTimeout <= 0 时用 store.DefaultBusyTimeout（1s）。
	BusyTimeout *Duration `yaml:"busy_timeout"`
}

// History 描述对话历史的存储（F-38 的落盘选项）。
type History struct {
	// File 是**旧版 JSONL 历史的一次性导入源**；新数据一律落 SQLite（见 store.path）。
	// 导入由启动时执行，幂等且只在库为空时触发；为空或不存在的文件都不影响运行。
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
	// Memory 为 true 时启用长期记忆（落 SQLite，见 store.path；F-87/F-49）。
	Memory *bool `yaml:"memory"`
	// MemoryMax 是记忆条数上限（每个作用域），默认 64。
	MemoryMax *int `yaml:"memory_max"`
	// MemoryFile 是**旧版记忆 JSONL 的一次性导入源**（导入进扁平记忆表，幂等）。
	// 记忆本身一直落 SQLite；为空或文件不存在就不做导入。
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
	// Reflect 是空闲时的记忆反思（F-48 的延伸）。**默认关闭**：它会主动花钱。
	Reflect Reflect `yaml:"reflect"`
}

// Reflect 描述空闲反思：当一段对话滑出上下文窗口、且会话安静下来后，
// 把那段内容交给模型提炼成长期记忆。
//
// 它是**后台花钱**的能力，因此四道闸门都可配，默认值一律偏保守：
// 宁可漏记，也不让空闲任务把额度烧掉。
type Reflect struct {
	// Enabled 为 true 时启用；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// Every 是扫描间隔；<=0 时用 1m。
	Every *Duration `yaml:"every"`
	// IdleAfter 是"这轮聊完了"的静默阈值；<=0 时用 5m。
	IdleAfter *Duration `yaml:"idle_after"`
	// MinInterval 是同一会话两次反思的最小间隔；<=0 时用 30m。
	MinInterval *Duration `yaml:"min_interval"`
	// MaxItems 是单次送进模型的最大条目数；<=0 时用 40。
	MaxItems *int `yaml:"max_items"`
	// MaxFacts 是单次最多写入的事实数；<=0 时用 5。
	MaxFacts *int `yaml:"max_facts"`
	// DailyBudget 是**每日反思调用上限**；<=0 时用 50。
	// 这是花钱的硬闸门：额度用尽时后台任务完全不调模型。
	DailyBudget *int `yaml:"daily_budget"`
	// Timeout 是单次反思的总预算；<=0 时用 45s。
	Timeout *Duration `yaml:"timeout"`
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
	// F-32 上下文预算：MaxContext <= 0（默认）时**不做**预算裁剪，行为与之前一致。
	// 开启后每次请求按"窗口 - 输出预留 - 工具预留"裁剪，超长历史不再被服务端拒绝。
	// MaxContext 是模型上下文窗口（token）。
	MaxContext *int `yaml:"max_context"`
	// ReserveOutput 是留给输出的 token；<=0 时取 1024。
	ReserveOutput *int `yaml:"reserve_output"`
	// ReserveTools 是留给工具 schema 的 token；<=0 时取 512。
	ReserveTools *int `yaml:"reserve_tools"`
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

// RetrievalTree 是摘要树召回（F-52）的配置。
//
// 它是混合检索之外**独立的一条召回源**：在摘要层检索，宏观问题因此有机会
// 命中上层摘要，而不是只能在原文里找关键词。
type RetrievalTree struct {
	// Enabled 为 true 时启用；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// MaxLevels 是最多再构建几层；<=0 时用 3。
	MaxLevels *int `yaml:"max_levels"`
	// MinCluster 是成簇下限；<=1 时用 2（小于 2 会退化成单链）。
	MinCluster *int `yaml:"min_cluster"`
	// Branching 是单簇节点上限；<=1 时用 4。
	Branching *int `yaml:"branching"`
	// MaxNodes 是全树节点上限；<=0 时用 10000。
	MaxNodes *int `yaml:"max_nodes"`
	// ClusterThreshold 是归入既有簇的最低相似度；<0 时用 0.5。
	ClusterThreshold *float64 `yaml:"cluster_threshold"`
}

// Retrieval 描述混合检索（F-51）。默认关闭。
//
// 关闭是默认值：它改变 recall_history 的排序口径（关键词 + 向量融合），
// 而两者各有偏好——要换就明确换，而不是让升级悄悄改变召回结果。
type Retrieval struct {
	// Enabled 为 true 时启用；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// KeywordWeight 是关键词一路的权重；未配置或为 0 时取 1.0，负值表示关闭该路。
	KeywordWeight *float64 `yaml:"keyword_weight"`
	// VectorWeight 是向量一路的权重；未配置或为 0 时取 1.0，负值表示关闭该路。
	VectorWeight *float64 `yaml:"vector_weight"`
	// TopK 是最终返回条数；<=0 时用 5。调用方给出的 limit 优先。
	TopK *int `yaml:"top_k"`
	// CandidateK 是每路候选数；<=0 时用 20。
	CandidateK *int `yaml:"candidate_k"`
	// Tree 是摘要树召回（F-52）；仅在 Enabled 且本配置启用时生效。
	Tree RetrievalTree `yaml:"tree"`
}

// Stream 描述流式增量发送（F-64）。默认关闭。
//
// 关闭是默认值：逐条发送会改变用户看到回复的节奏，也可能触发平台限流；
// 开启前应当确认目标平台能接受高频短消息。
type Stream struct {
	// Enabled 为 true 时启用；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// MaxChars 是触发发送的长度阈值；<=0 时用 llm.DefaultFlushChars（40）。
	MaxChars *int `yaml:"max_chars"`
	// MaxInterval 是"距上次发送超过此时长即触发"；<=0 时用 800ms。
	MaxInterval *Duration `yaml:"max_interval"`
	// MinInterval 是发送频率上限；<=0 时用 800ms。
	MinInterval *Duration `yaml:"min_interval"`
	// FirstMinChars 是首段最小长度；<=0 时用 llm.DefaultFirstMinChars（8）。
	FirstMinChars *int `yaml:"first_min_chars"`
	// EditMessages 为 true 时用"编辑消息"表达改写；平台不支持时会中止该流。
	EditMessages *bool `yaml:"edit_messages"`
	// TypingHint 非空时在首段正文前单发一条提示（如"正在输入…"）。
	TypingHint string `yaml:"typing_hint"`
}

// Semcache 描述语义缓存（F-63）。默认关闭。
//
// 关闭是刻意的默认：缓存会改变"同一句话在不同时刻得到什么回答"，
// 而它省下的只是重复问题的 token。要开就明确开。
type Semcache struct {
	// Enabled 为 true 时启用；未配置时默认关闭。
	Enabled *bool `yaml:"enabled"`
	// Threshold 是命中所需的最低相似度（0,1]；<=0 时用 0.95。
	Threshold *float64 `yaml:"threshold"`
	// TTLSeconds 是条目生存时间；<=0 时用 3600 秒。
	TTLSeconds *int `yaml:"ttl_seconds"`
	// MaxEntries 是容量上限；<=0 时用 4096。
	MaxEntries *int `yaml:"max_entries"`
	// SkipWords 是易变话题关键词（子串匹配），在**内置跳过列表之外**追加。
	SkipWords []string `yaml:"skip_words"`
	// SkipPatterns 是附加的正则跳过规则；语法错误会让启动失败（fail-fast）。
	SkipPatterns []string `yaml:"skip_patterns"`
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
	// LegacySuperUsers 只用于**捕获已移除的旧键** moderation.super_users。
	//
	// 它不是有效配置：一旦出现就报错并给出迁移指引，而不是被静默忽略——
	// 静默忽略会表现成"我明明是超管，怎么命令不管用了"，比启动失败难查得多。
	// 超管只有一个位置：access.roles.superuser。
	LegacySuperUsers []int64 `yaml:"super_users,omitempty"`
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
