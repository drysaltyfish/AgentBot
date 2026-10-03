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
	Provider      string    `yaml:"provider"`
	Model         string    `yaml:"model"`
	BaseURL       string    `yaml:"base_url"`
	APIKey        *string   `yaml:"api_key"`
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
