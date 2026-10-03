package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Test_F61_MaskKeepsPrefixAndLength 覆盖验收：脱敏只保留前 4 位与长度。
func Test_F61_MaskKeepsPrefixAndLength(t *testing.T) {
	t.Parallel()
	if got := Mask(""); got != "" {
		t.Fatalf("空串应返回空串: %q", got)
	}
	if got := Mask("abc"); got != "***" {
		t.Fatalf("短语应全遮: %q", got)
	}
	got := Mask("sk-1234567890")
	if !strings.HasPrefix(got, "sk-1") || !strings.Contains(got, "len=13") {
		t.Fatalf("脱敏格式不符: %q", got)
	}
	if strings.Contains(got, "2345") {
		t.Fatalf("脱敏后不应残留密钥内容: %q", got)
	}
}

// Test_F61_ScrubReplacesCommonShapes 覆盖验收：常见密钥形态在日志里被替换。
func Test_F61_ScrubReplacesCommonShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		keep   string
		remove string
	}{
		{"bearer", "Authorization: Bearer abcdefghijklmn", "Bearer", "abcdefghijklmn"},
		{"sk", "key=sk-abcdef123456", "sk-", "sk-abcdef123456"},
		{"akid", "cred AKIDABCDEFGHIJKL", "AKID", "AKIDABCDEFGHIJKL"},
		{"named", "api_key=supersecretvalue", "api_key", "supersecretvalue"},
		{"token", "access_token: anothersecret", "access_token", "anothersecret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in)
			if !strings.Contains(got, tc.keep) {
				t.Fatalf("应保留标识 %q: %q", tc.keep, got)
			}
			if strings.Contains(got, tc.remove) {
				t.Fatalf("应遮掉密钥内容 %q: %q", tc.remove, got)
			}
		})
	}
	if got := Scrub("今天天气不错，群里聊得很开心"); got != "今天天气不错，群里聊得很开心" {
		t.Fatalf("普通文本不应被改动: %q", got)
	}
}

// Test_F61_FilePermissionWarning 覆盖边界：权限不符只告警，不阻断。
func Test_F61_FilePermissionWarning(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 POSIX 权限位，无法断言")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if PermissionWarning(path, info) == "" {
		t.Fatalf("0644 应产生告警")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	info, _ = os.Stat(path)
	if w := PermissionWarning(path, info); w != "" {
		t.Fatalf("0600 不应告警: %s", w)
	}
}

// Test_F61_ResolvePriority 覆盖验收：环境变量 > 密钥文件 > 内联。
func Test_F61_ResolvePriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("AGENTBOT_TEST_KEY", "from-env")
	v, err := Resolve(ResolveOptions{EnvVar: "AGENTBOT_TEST_KEY", FilePath: path, Inline: "inline"})
	if err != nil || v.Source != SourceEnv || v.Secret != "from-env" {
		t.Fatalf("环境变量应优先: %+v err=%v", v, err)
	}

	t.Setenv("AGENTBOT_TEST_KEY", "")
	v, err = Resolve(ResolveOptions{EnvVar: "AGENTBOT_TEST_KEY", FilePath: path, Inline: "inline"})
	if err != nil || v.Source != SourceFile || v.Secret != "from-file" {
		t.Fatalf("环境变量为空时应退到文件: %+v err=%v", v, err)
	}
	if v.Warning != "" {
		t.Fatalf("0600 文件不应有告警: %s", v.Warning)
	}

	v, err = Resolve(ResolveOptions{Inline: "inline"})
	if err != nil || v.Source != SourceInline || v.Warning == "" {
		t.Fatalf("内联应带告警: %+v err=%v", v, err)
	}

	if _, err := Resolve(ResolveOptions{Required: true}); err == nil {
		t.Fatalf("Required 且无来源应报错")
	}
}

// Test_F61_EmptyFileIsAnError 覆盖边界：空密钥文件必须报错（不得当成空密钥继续）。
func Test_F61_EmptyFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Resolve(ResolveOptions{FilePath: path, Required: true}); err == nil {
		t.Fatalf("空文件应报错")
	}
}
