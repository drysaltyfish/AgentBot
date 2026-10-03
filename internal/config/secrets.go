package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/secrets"
	"gopkg.in/yaml.v3"
)

// secretFields 是唯一一份“哪些字段是敏感项”的清单：
// expandSecrets 用它展开 ${VAR} 引用，Redacted 用它脱敏。
// 新增敏感字段时只改这里，两处行为自动保持一致。
var secretFields = []struct {
	// path 是错误信息里使用的配置路径。
	path string
	// ptr 指向 Config 上的 *string 字段（便于原地替换）。
	ptr func(*Config) **string
}{
	{"transport.access_token", func(c *Config) **string { return &c.Transport.AccessToken }},
	{"transport.signature_secret", func(c *Config) **string { return &c.Transport.SignatureSecret }},
	{"llm.api_key", func(c *Config) **string { return &c.LLM.APIKey }},
}

// expandSecrets 就地展开敏感字段里的 ${VAR} 引用，让密钥不必落盘。
func (c *Config) expandSecrets() error {
	for _, f := range secretFields {
		p := f.ptr(c)
		if *p == nil {
			continue
		}
		v, err := expandSecret(f.path, **p)
		if err != nil {
			return err
		}
		**p = v
	}
	return nil
}

// expandSecret 解析 ${VAR} 形态的引用。
//
// 未设置的环境变量直接报错而不是静默变成空串——否则会表现为“密钥没生效”，
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

// Redact 按 F-61 的约定脱敏：只保留前 4 位与长度。
//
// 实现在 internal/secrets：配置导出、日志清洗、审计共用同一份规则，
// 避免"配了脱敏但漏了一个出口"。
func Redact(s string) string { return secrets.Mask(s) }

// Redacted 返回一份用于打印的副本，所有敏感项已脱敏。
func (c *Config) Redacted() *Config {
	cp := *c
	for _, f := range secretFields {
		p := f.ptr(&cp)
		if *p == nil {
			continue
		}
		v := Redact(**p)
		*p = &v
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
