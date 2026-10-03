// Package config 实现配置装载、校验与脱敏（FEATURES.md F-25）。
//
// 约定：区分“未设置”与“设置为零值”一律用 *T 指针表达，禁止用“0 即未设置”。
package config

import (
	"errors"
	"fmt"
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

// 回复策略取值。
const (
	// ReplyAlways 无条件回复。
	ReplyAlways = "always"
	// ReplyNever 永不回复。
	ReplyNever = "never"
	// ReplyOnMention 仅在 @ 机器人时回复（群聊默认，避免刷屏）。
	ReplyOnMention = "on_mention"
)

// Config 是 AgentBot 的全部配置。
type Config struct {
	Store        Store        `yaml:"store"`
	Transport    Transport    `yaml:"transport"`
	LLM          LLM          `yaml:"llm"`
	Agent        Agent        `yaml:"agent"`
	History      History      `yaml:"history"`
	Behavior     Behavior     `yaml:"behavior"`
	Prompt       Prompt       `yaml:"prompt"`
	Policy       Policy       `yaml:"policy"`
	Log          Log          `yaml:"log"`
	Shutdown     Shutdown     `yaml:"shutdown"`
	RateLimit    RateLimit    `yaml:"ratelimit"`
	Toggle       Toggle       `yaml:"toggle"`
	Audit        Audit        `yaml:"audit"`
	Ops          Ops          `yaml:"ops"`
	Singleflight Singleflight `yaml:"singleflight"`
}
