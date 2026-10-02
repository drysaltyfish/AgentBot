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
	Prompt    Prompt    `yaml:"prompt"`
	Policy    Policy    `yaml:"policy"`
	Log       Log       `yaml:"log"`
	Shutdown  Shutdown  `yaml:"shutdown"`
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
	return &Config{
		Transport: Transport{Mode: "wsclient", Backoff: &backoff},
		LLM:       LLM{Provider: "openai", BaseURL: "https://api.openai.com/v1", Timeout: &timeout},
		Prompt:    Prompt{Dir: "prompts"},
		Policy:    Policy{File: "actions.yaml"},
		Log:       Log{Level: "info", Format: "json", Components: map[string]string{}, QueueSize: &queue},
		Shutdown:  Shutdown{Timeout: &sdTimeout},
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
	return cfg, nil
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
