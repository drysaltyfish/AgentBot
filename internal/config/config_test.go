package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_F25_DefaultIsValid(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Transport.URL = "ws://127.0.0.1:3001"
	cfg.LLM.Model = "gpt-4o-mini"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: actual=%v expected=nil", err)
	}
}

func Test_F25_ParseDistinguishesUnsetFromExplicitZero(t *testing.T) {
	t.Parallel()

	without, err := Parse([]byte("transport:\n  mode: wsclient\n  url: ws://x\n"))
	if err != nil {
		t.Fatalf("parse without self_id: actual=%v expected=nil", err)
	}
	if without.Transport.SelfID != nil {
		t.Fatalf("unset self_id: actual=%v expected=nil", *without.Transport.SelfID)
	}

	withZero, err := Parse([]byte("transport:\n  mode: wsclient\n  url: ws://x\n  self_id: 0\n"))
	if err != nil {
		t.Fatalf("parse with self_id: 0: actual=%v expected=nil", err)
	}
	if withZero.Transport.SelfID == nil {
		t.Fatalf("explicit self_id: 0 was treated as unset: actual=nil expected=&0")
	}
	if *withZero.Transport.SelfID != 0 {
		t.Fatalf("explicit self_id: 0: actual=%d expected=0", *withZero.Transport.SelfID)
	}
}

func Test_F25_ValidateReportsAllProblemsAtOnce(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Transport.Mode = "carrier-pigeon"
	cfg.Transport.URL = ""
	cfg.LLM.Provider = ""
	cfg.LLM.Model = ""
	cfg.Log.Level = "verbose"
	cfg.Log.Format = "xml"

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error: actual=nil expected=error")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error does not wrap ErrInvalid: actual=%v", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type: actual=%T expected=*config.ValidationError", err)
	}
	for _, path := range []string{"transport.mode", "llm.provider", "llm.model", "log.level", "log.format"} {
		if !ve.Has(path) {
			t.Fatalf("missing problem %s in %v (expected all problems reported at once)", path, ve)
		}
	}
	if len(ve.Problems) < 5 {
		t.Fatalf("problem count: actual=%d expected>=5", len(ve.Problems))
	}
}

// Test_F25_UnimplementedInboundModesAreRejected 钉住"校验不能放行跑不起来的模式"。
//
// 过去这两个模式只校验 access_token，于是 mode=wsserver 能通过校验、
// `--check-config` 返回 0，进程随后带着空 URL 打印 "agentbot started" 并无限重连——
// 与真正的网络故障无法区分。宁可启动失败也不要带病启动。
//
// 注意：F-25/F-80 的"入站必须配 access_token"是**真正实现入站驱动之后**才需要恢复的检查；
// 现在这两个模式根本没有入站监听，谈 token 没有意义。
func Test_F25_UnimplementedInboundModesAreRejected(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"wsserver", "http"} {
		cfg := Default()
		cfg.Transport.Mode = mode
		cfg.LLM.Model = "m"
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("mode=%s 尚未实现，必须被拒绝: actual=nil", mode)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || !ve.Has("transport.mode") {
			t.Fatalf("mode=%s: actual=%v expected problem transport.mode", mode, err)
		}
	}

	// 即使配了 token 也要拒绝：问题不在鉴权，而在这个模式跑不起来。
	cfg := Default()
	cfg.Transport.Mode = "wsserver"
	cfg.LLM.Model = "m"
	tok := "tok"
	cfg.Transport.AccessToken = &tok
	if err := cfg.Validate(); err == nil {
		t.Fatalf("mode=wsserver 配上 token 仍然必须被拒绝")
	}
}

func Test_F25_UnknownFieldIsRejected(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("transport:\n  mode: wsclient\n  urll: ws://x\n"))
	if err == nil {
		t.Fatalf("unknown field urll: actual=nil expected=error")
	}
	if !strings.Contains(err.Error(), "urll") {
		t.Fatalf("error should name the offending field: actual=%v", err)
	}
}

func Test_F25_DurationParsing(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte("llm:\n  model: m\n  timeout: 1500ms\nshutdown:\n  timeout: 5s\n"))
	if err != nil {
		t.Fatalf("parse durations: actual=%v expected=nil", err)
	}
	if cfg.LLM.Timeout == nil || cfg.LLM.Timeout.D != 1500*time.Millisecond {
		t.Fatalf("llm.timeout: actual=%v expected=1.5s", cfg.LLM.Timeout)
	}
	if cfg.Shutdown.Timeout == nil || cfg.Shutdown.Timeout.D != 5*time.Second {
		t.Fatalf("shutdown.timeout: actual=%v expected=5s", cfg.Shutdown.Timeout)
	}

	if _, err := Parse([]byte("llm:\n  model: m\n  timeout: soon\n")); err == nil {
		t.Fatalf("invalid duration: actual=nil expected=error")
	}
}

func Test_F25_Redact(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "***"},
		{"abcd", "****"},
		{"sk-1234567890", "sk-1***（len=13）"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Fatalf("Redact(%q): actual=%q expected=%q", c.in, got, c.want)
		}
	}
}

func Test_F25_RedactedYAMLDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()
	const secret = "sk-super-secret-value-1234567890"
	cfg := Default()
	cfg.LLM.Model = "m"
	key := secret
	cfg.LLM.APIKey = &key
	token := "bot-access-token-abcdef"
	cfg.Transport.AccessToken = &token

	out, err := cfg.RedactedYAML()
	if err != nil {
		t.Fatalf("RedactedYAML: actual=%v expected=nil", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("redacted YAML leaks API key")
	}
	if strings.Contains(out, token) {
		t.Fatalf("redacted YAML leaks access token")
	}
	if !strings.Contains(out, "sk-s***") {
		t.Fatalf("redacted YAML missing masked key: %s", out)
	}

	orig, err := Default().RedactedYAML()
	if err != nil || orig == "" {
		t.Fatalf("RedactedYAML on defaults: actual=%v", err)
	}
}

const deepseekBaseYAML = `transport:
  mode: wsclient
  url: ws://127.0.0.1:3001
llm:
  provider: deepseek
  model: deepseek-flash
`

func Test_ConfigSecretsExpandFromEnvironment(t *testing.T) {
	t.Setenv("AGENTBOT_TEST_KEY", "sk-from-env")
	cfg, err := Parse([]byte(deepseekBaseYAML + `  api_key: ${AGENTBOT_TEST_KEY}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.LLM.APIKey == nil || *cfg.LLM.APIKey != "sk-from-env" {
		t.Fatalf("env expansion failed: %v", cfg.LLM.APIKey)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func Test_ConfigUnsetEnvVarFailsLoudly(t *testing.T) {
	_, err := Parse([]byte(deepseekBaseYAML + `  api_key: ${AGENTBOT_SURELY_UNSET_VAR}
`))
	if err == nil {
		t.Fatalf("referencing an unset env var must fail instead of silently becoming empty")
	}
	if !strings.Contains(err.Error(), "AGENTBOT_SURELY_UNSET_VAR") {
		t.Fatalf("error must name the missing variable: %v", err)
	}
}

func Test_ConfigBehaviorDefaultsToGroupOnMention(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Behavior.Private != ReplyAlways {
		t.Fatalf("private default: %q", cfg.Behavior.Private)
	}
	if cfg.Behavior.Group != ReplyOnMention {
		t.Fatalf("group default: %q", cfg.Behavior.Group)
	}
}

func Test_ConfigRejectsUnknownReplyPolicyAndEffort(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML + `behavior:
  private: sometimes
  group: shouting
llm2: {}
`))
	_ = cfg
	if err == nil {
		t.Fatalf("unknown field llm2 should be rejected by KnownFields")
	}

	cfg2, err := Parse([]byte(deepseekBaseYAML + `behavior:
  private: sometimes
  group: shouting
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = cfg2.Validate()
	if err == nil {
		t.Fatalf("invalid reply policy must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type: %T", err)
	}
	if !ve.Has("behavior.private") || !ve.Has("behavior.group") {
		t.Fatalf("expected both behavior paths to be reported: %v", ve.Problems)
	}
}

func Test_ConfigRejectsUnknownReasoningEffort(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML + `  reasoning_effort: extreme
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatalf("invalid reasoning_effort must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || !ve.Has("llm.reasoning_effort") {
		t.Fatalf("expected llm.reasoning_effort problem: %v", err)
	}
}

func Test_ConfigAcceptsDeepSeekThinkingSettings(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML + `  thinking: true
  reasoning_effort: low
  history_turns: 12
  system_prompt: "你是助手"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.LLM.Thinking == nil || !*cfg.LLM.Thinking {
		t.Fatalf("thinking not parsed")
	}
	if cfg.LLM.ReasoningEffort != "low" || cfg.LLM.HistoryTurns == nil || *cfg.LLM.HistoryTurns != 12 {
		t.Fatalf("deepseek settings not parsed: %+v", cfg.LLM)
	}
}

func Test_ConfigSystemPromptFileMustExist(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML + `  system_prompt_file: prompts/definitely-missing-file.md
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatalf("a missing system_prompt_file must fail validation")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || !ve.Has("llm.system_prompt_file") {
		t.Fatalf("expected llm.system_prompt_file problem: %v", err)
	}
}

func Test_ConfigSystemPromptFileAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(path, []byte("你是香橙娘"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, err := Parse([]byte(deepseekBaseYAML + "  system_prompt_file: " + path + "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.LLM.SystemPromptFile == nil || *cfg.LLM.SystemPromptFile != path {
		t.Fatalf("system_prompt_file not parsed: %v", cfg.LLM.SystemPromptFile)
	}
}

func Test_ConfigRejectsBadSplitSettings(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML + `behavior:
  split_delay: 30s
  max_segments: 0
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatalf("invalid split settings must be rejected")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type: %T", err)
	}
	if !ve.Has("behavior.split_delay") || !ve.Has("behavior.max_segments") {
		t.Fatalf("expected both split problems: %v", ve.Problems)
	}
}

func Test_ConfigSplitDefaultsAreOn(t *testing.T) {
	cfg, err := Parse([]byte(deepseekBaseYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Behavior.SplitOnBlankLine == nil || !*cfg.Behavior.SplitOnBlankLine {
		t.Fatalf("split_on_blank_line should default to true")
	}
	if cfg.Behavior.MaxSegments == nil || *cfg.Behavior.MaxSegments != 4 {
		t.Fatalf("max_segments default: %v", cfg.Behavior.MaxSegments)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}
