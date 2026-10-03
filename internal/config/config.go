// Package config 实现配置装载、校验与脱敏（FEATURES.md F-25）。
//
// 约定：区分"未设置"与"设置为零值"一律用 *T 指针表达，禁止用"0 即未设置"。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrInvalid 是配置校验失败的哨兵，可用 errors.Is 判定。
var ErrInvalid = errors.New("invalid config")

// Duration 让 YAML 里可以直接写 "10s"。
type Duration struct{ D time.Duration }

// UnmarshalYAML 解析 "10s" / "1500ms" 形式的值。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string such as `10s`: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration must not be negative: %q", s)
	}
	d.D = parsed
	return nil
}

// MarshalYAML 使 Duration 以字符串形态回写。
func (d Duration) MarshalYAML() (any, error) { return d.D.String(), nil }

// String 实现 fmt.Stringer。
func (d Duration) String() string { return d.D.String() }

// Config 是 AgentBot 的全部配置。
type Config struct {
	Transport Transport `yaml:"transport"`
	LLM       LLM       `yaml:"llm"`
	Agent     Agent     `yaml:"agent"`
	History   History   `yaml:"history"`
	Behavior  Behavior  `yaml:"behavior"`
	Prompt    Prompt    `yaml:"prompt"`
	Policy    Policy    `yaml:"policy"`
	Log       Log       `yaml:"log"`
	Shutdown  Shutdown  `yaml:"shutdown"`
}

// 回复策略取值。
const (
	// ReplyAlways 无条件回复。
	ReplyAlways = "always"
	// ReplyNever 永不回复。
	ReplyNever = "never"
	// ReplyOnMention 仅在 @ 机器人时回复（群聊默认，避免刷屏）。
	ReplyOnMention = "on_mention"
)

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

// Default 返回带默认值的配置。可选字段用指针，nil 表示"未设置"。
func Default() *Config {
	backoff := Duration{D: time.Second}
	timeout := Duration{D: 30 * time.Second}
	queue := 1024
	sdTimeout := Duration{D: 10 * time.Second}
	historyTurns := 20
	maxIterations := 10
	stepTimeout := Duration{D: 30 * time.Second}
	approvalTimeout := Duration{D: 60 * time.Second}
	virtualActions := true
	memoryOn := true
	memoryMax := 64
	splitOnBlank := true
	splitDelay := Duration{D: 400 * time.Millisecond}
	maxSegments := 4
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
	}
}

// Load 读取并解析配置文件（不做校验）。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse 解析配置字节；未知字段直接报错，避免拼错的配置项被静默忽略。
func Parse(raw []byte) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.expandSecrets(); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// expandSecrets 就地展开敏感字段里的 ${VAR} 引用，让密钥不必落盘。
func (c *Config) expandSecrets() error {
	fields := []struct {
		path string
		ptr  **string
	}{
		{"transport.access_token", &c.Transport.AccessToken},
		{"transport.signature_secret", &c.Transport.SignatureSecret},
		{"llm.api_key", &c.LLM.APIKey},
	}
	for _, f := range fields {
		if f.ptr == nil || *f.ptr == nil {
			continue
		}
		v, err := expandSecret(f.path, **f.ptr)
		if err != nil {
			return err
		}
		**f.ptr = v
	}
	return nil
}

// expandSecret 解析 ${VAR} 形态的引用。
//
// 未设置的环境变量直接报错而不是静默变成空串——否则会表现为"密钥没生效"，
// 排查成本远高于启动即失败。
func expandSecret(path, raw string) (string, error) {
	if !strings.Contains(raw, "${") {
		return raw, nil
	}
	var missing []string
	out := os.Expand(raw, func(name string) string {
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%s 引用了未设置的环境变量: %s", path, strings.Join(missing, ", "))
	}
	return out, nil
}

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

	if len(problems) == 0 {
		return nil
	}
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return &ValidationError{Problems: problems}
}

// Redact 按 F-61 的约定脱敏：只保留前 4 位与长度。
func Redact(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + "***（len=" + strconv.Itoa(len(r)) + "）"
}

// Redacted 返回一份用于打印的副本，所有敏感项已脱敏。
func (c *Config) Redacted() *Config {
	cp := *c
	if cp.Transport.AccessToken != nil {
		v := Redact(*cp.Transport.AccessToken)
		cp.Transport.AccessToken = &v
	}
	if cp.Transport.SignatureSecret != nil {
		v := Redact(*cp.Transport.SignatureSecret)
		cp.Transport.SignatureSecret = &v
	}
	if cp.LLM.APIKey != nil {
		v := Redact(*cp.LLM.APIKey)
		cp.LLM.APIKey = &v
	}
	return &cp
}

// RedactedYAML 把脱敏后的配置序列化成 YAML，供 --check-config 打印。
func (c *Config) RedactedYAML() (string, error) {
	out, err := yaml.Marshal(c.Redacted())
	if err != nil {
		return "", fmt.Errorf("marshal redacted config: %w", err)
	}
	return string(out), nil
}
