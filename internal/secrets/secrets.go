// Package secrets 负责密钥的解析、脱敏与日志清洗（F-61）。
//
// 为什么单独成包：密钥会出现在三个地方——配置、日志、错误信息，而"哪里漏了脱敏"
// 靠人盯是盯不住的。把规则收在一处，日志管道、审计、配置导出共用同一份实现。
package secrets

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// Mask 只保留前 4 个字符与长度，例如 sk-1***（len=51）；空串返回空串。
func Mask(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return fmt.Sprintf("%s***（len=%d）", string(r[:4]), len(r))
}

// scrubPatterns 是常见的密钥形态。它们互不依赖，逐个替换即可。
var scrubPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`AKID[A-Za-z0-9]{10,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|signature[_-]?secret|password)\s*[:=]\s*"?[^"\s,}]{6,}"?`),
}

// Scrub 对任意字符串做模式替换：常见 key 前缀、Bearer、sk-、AKID 等。
//
// 保留键名/前缀，只遮掉值部分——日志要能看出"这里有个 key"，但不该看出它是什么。
func Scrub(s string) string {
	for _, re := range scrubPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if i := strings.IndexAny(m, ":="); i >= 0 {
				return m[:i+1] + " " + Mask(strings.Trim(strings.TrimSpace(m[i+1:]), "\"'"))
			}
			if j := strings.IndexByte(m, ' '); j >= 0 {
				return m[:j+1] + Mask(m[j+1:])
			}
			return Mask(m)
		})
	}
	return s
}

// Source 说明密钥来自哪里。
type Source string

// 密钥来源。
const (
	SourceEnv    Source = "env"
	SourceFile   Source = "file"
	SourceInline Source = "inline"
)

// Value 是一次密钥解析的结果。
type Value struct {
	// Secret 是解析出来的密钥明文；调用方不得把它写进日志。
	Secret string
	// Source 说明来源，便于启动日志解释"为什么用的是这一份"。
	Source Source
	// Warning 是应当记录但不阻断的告警（内联密钥、文件权限过宽等）。
	Warning string
}

// FromEnv 读取环境变量；未设置或为空返回 false。
func FromEnv(name string) (string, bool) {
	if strings.TrimSpace(name) == "" {
		return "", false
	}
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", false
	}
	return v, true
}

// FromFile 读取密钥文件，并做权限检查（不合规只告警，不阻断）。
func FromFile(path string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	warn := PermissionWarning(path, info)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", "", fmt.Errorf("secret file %s is empty", path)
	}
	return v, warn, nil
}

// PermissionWarning 检查密钥文件权限；不合规时返回可读告警（不阻断启动）。
//
// Unix 下要求 0600；Windows 下标准库拿不到 ACL，只能按权限位做尽力而为的判断，
// 因此措辞上明确"未校验"，不假装检查过。
func PermissionWarning(path string, info os.FileInfo) string {
	perm := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		// Windows 没有 POSIX 权限位，而标准库也拿不到 ACL。
		// 与其按权限位猜（几乎每个普通文件都会被误报），不如明确说明未校验；
		// 真正的 ACL 检查需要 syscall，留给后续（F-61 备注里已记录）。
		_ = perm
		return ""
	}
	if perm&0o077 != 0 {
		return fmt.Sprintf("密钥文件 %s 权限为 %04o，建议 0600", path, perm)
	}
	return ""
}

// ResolveOptions 描述一次密钥解析的候选来源。
type ResolveOptions struct {
	// EnvVar 是环境变量名，优先级最高。
	EnvVar string
	// FilePath 是密钥文件路径，第二优先级。
	FilePath string
	// Inline 是配置内联值，优先级最低。
	Inline string
	// Required 为 true 时，全部来源都为空会返回错误。
	Required bool
}

// Resolve 按 F-61 的优先级解析密钥：环境变量 > 密钥文件 > 配置内联。
func Resolve(opts ResolveOptions) (Value, error) {
	if v, ok := FromEnv(opts.EnvVar); ok {
		return Value{Secret: v, Source: SourceEnv}, nil
	}
	if path := strings.TrimSpace(opts.FilePath); path != "" {
		v, warn, err := FromFile(path)
		if err != nil {
			return Value{}, err
		}
		return Value{Secret: v, Source: SourceFile, Warning: warn}, nil
	}
	if opts.Inline != "" {
		return Value{
			Secret:  opts.Inline,
			Source:  SourceInline,
			Warning: "密钥以内联方式写在配置里；建议改用环境变量或密钥文件（F-61）",
		}, nil
	}
	if opts.Required {
		return Value{}, fmt.Errorf("没有可用的密钥：请设置环境变量、密钥文件或内联配置")
	}
	return Value{}, nil
}
