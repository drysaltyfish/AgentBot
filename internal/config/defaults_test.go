package config

import (
	"strings"
	"testing"
	"time"
)

func Test_DurationOrResolvesUnsetAndNonPositive(t *testing.T) {
	t.Parallel()
	fallback := 5 * time.Second
	if got := (*Duration)(nil).Or(fallback); got != fallback {
		t.Fatalf("nil: actual=%v expected=%v", got, fallback)
	}
	for _, d := range []Duration{{D: 0}, {D: -time.Second}} {
		if got := (&d).Or(fallback); got != fallback {
			t.Fatalf("%v: actual=%v expected=%v", d, got, fallback)
		}
	}
	d := Duration{D: 2 * time.Second}
	if got := (&d).Or(fallback); got != 2*time.Second {
		t.Fatalf("explicit: actual=%v expected=2s", got)
	}
	var nilPtr *Duration
	if got := nilPtr.Or(fallback); got != fallback {
		t.Fatalf("nil method receiver: actual=%v expected=%v", got, fallback)
	}
}

func Test_EffectiveAccessorsResolveModuleDefaults(t *testing.T) {
	t.Parallel()
	var c Config
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"store.busy_timeout", c.Store.BusyTimeoutOr(time.Second), time.Second},
		{"history.retention", c.History.EffectiveRetention(), 400},
		{"agent.max_iterations", c.Agent.MaxIterationsOr(10), 10},
		{"agent.step_timeout", c.Agent.StepTimeoutOr(30 * time.Second), 30 * time.Second},
		{"agent.approval_timeout", c.Agent.ApprovalTimeoutOr(60 * time.Second), 60 * time.Second},
		{"agent.memory", c.Agent.EffectiveMemory(), true},
		{"agent.virtual_actions", c.Agent.EffectiveVirtualActions(), true},
		{"agent.memory_max", c.Agent.EffectiveMemoryMax(), 64},
		{"agent.memory_judge", c.Agent.MemoryJudge.EffectiveEnabled(), true},
		{"agent.proactive_memory", c.Agent.ProactiveMemory.EffectiveEnabled(), true},
		{"agent.tool_hint", c.Agent.ToolHint.EffectiveEnabled(), true},
		{"behavior.private", c.Behavior.EffectivePrivate(), ReplyAlways},
		{"behavior.group", c.Behavior.EffectiveGroup(), ReplyOnMention},
		{"behavior.split_on_blank_line", c.Behavior.EffectiveSplitOnBlankLine(), true},
		{"behavior.split_delay", c.Behavior.EffectiveSplitDelay(), 400 * time.Millisecond},
		{"behavior.max_segments", c.Behavior.EffectiveMaxSegments(), 4},
		{"transport.self_id", c.Transport.EffectiveSelfID(), int64(0)},
		{"llm.timeout", c.LLM.EffectiveTimeout(), 30 * time.Second},
		{"llm.history_turns", c.LLM.EffectiveHistoryTurns(), 20},
		{"llm.ambient_token_budget", c.LLM.AmbientTokenBudgetOr(1000), 1000},
		{"llm.ambient_max_chars", c.LLM.AmbientMaxCharsOr(500), 500},
		{"log.queue_size", c.Log.EffectiveQueueSize(), 1024},
		{"shutdown.timeout", c.Shutdown.EffectiveTimeout(), 10 * time.Second},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Fatalf("%s: actual=%v expected=%v", tc.name, tc.got, tc.want)
		}
	}
}

func Test_EffectiveAccessorsHonorExplicitValues(t *testing.T) {
	t.Parallel()
	one := 1
	small := 7
	delay := Duration{D: 0} // 显式 0 必须保留，不能当成未配置
	negative := -1
	off := false
	c := Config{
		Agent: Agent{
			Memory:          &off,
			VirtualActions:  &off,
			MaxIterations:   &small,
			MemoryMax:       &small,
			MemoryJudge:     MemoryJudge{Enabled: &off},
			ProactiveMemory: ProactiveMemory{Enabled: &off},
			ToolHint:        ToolHint{Enabled: &off},
		},
		Behavior: Behavior{
			Private:          ReplyNever,
			Group:            ReplyAlways,
			SplitOnBlankLine: &off,
			SplitDelay:       &delay,
			MaxSegments:      &small,
		},
		LLM: LLM{
			AmbientTokenBudget: &negative,
			AmbientMaxChars:    &small,
			HistoryTurns:       &one,
		},
	}
	if c.Agent.EffectiveMemory() || c.Agent.EffectiveVirtualActions() {
		t.Fatalf("explicit false must be honored")
	}
	if c.Agent.MemoryJudge.EffectiveEnabled() || c.Agent.ProactiveMemory.EffectiveEnabled() || c.Agent.ToolHint.EffectiveEnabled() {
		t.Fatalf("explicit false must be honored for nested options")
	}
	if got := c.Agent.MaxIterationsOr(10); got != 7 {
		t.Fatalf("agent.max_iterations: actual=%d expected=7", got)
	}
	if got := c.Agent.EffectiveMemoryMax(); got != 7 {
		t.Fatalf("agent.memory_max: actual=%d expected=7", got)
	}
	if got := c.Behavior.EffectivePrivate(); got != ReplyNever {
		t.Fatalf("behavior.private: actual=%q expected=%q", got, ReplyNever)
	}
	if got := c.Behavior.EffectiveGroup(); got != ReplyAlways {
		t.Fatalf("behavior.group: actual=%q expected=%q", got, ReplyAlways)
	}
	if c.Behavior.EffectiveSplitOnBlankLine() {
		t.Fatalf("explicit split_on_blank_line=false must be honored")
	}
	if got := c.Behavior.EffectiveSplitDelay(); got != 0 {
		t.Fatalf("explicit split_delay=0 must be preserved, actual=%v", got)
	}
	if got := c.Behavior.EffectiveMaxSegments(); got != 7 {
		t.Fatalf("behavior.max_segments: actual=%d expected=7", got)
	}
	if got := c.LLM.EffectiveHistoryTurns(); got != 1 {
		t.Fatalf("llm.history_turns: actual=%d expected=1", got)
	}
	if got := c.LLM.AmbientTokenBudgetOr(1000); got != -1 {
		t.Fatalf("negative ambient budget means no compression and must be preserved, actual=%d", got)
	}
	if got := c.LLM.AmbientMaxCharsOr(500); got != 7 {
		t.Fatalf("llm.ambient_max_chars: actual=%d expected=7", got)
	}
}

func Test_SecretFieldsAreTheSingleSourceOfTruth(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"transport.access_token":     true,
		"transport.signature_secret": true,
		"llm.api_key":                true,
	}
	if len(secretFields) != len(want) {
		t.Fatalf("secretFields count: actual=%d expected=%d", len(secretFields), len(want))
	}
	for _, f := range secretFields {
		if !want[f.path] {
			t.Fatalf("unexpected secret field %q", f.path)
		}
		delete(want, f.path)
	}
	if len(want) != 0 {
		t.Fatalf("secret fields not covered by the table: %v", want)
	}
}

func Test_RedactedMasksEverySecretFieldFromTheTable(t *testing.T) {
	t.Parallel()
	token, sig, key := "token-abcdef-123456", "sig-abcdef-123456", "sk-abcdef-123456"
	c := Default()
	c.Transport.AccessToken = &token
	c.Transport.SignatureSecret = &sig
	c.LLM.APIKey = &key
	out, err := c.RedactedYAML()
	if err != nil {
		t.Fatalf("RedactedYAML: %v", err)
	}
	for _, secret := range []string{token, sig, key} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted YAML leaks %q", secret)
		}
	}
	for _, masked := range []string{"toke***", "sig-***", "sk-a***"} {
		if !strings.Contains(out, masked) {
			t.Fatalf("redacted YAML is missing %q:\n%s", masked, out)
		}
	}
	if token == "" || c.Transport.AccessToken == nil || *c.Transport.AccessToken != token {
		t.Fatalf("Redacted must not mutate the receiver")
	}
}

func Test_ParseExpandsEverySecretFieldFromTheTable(t *testing.T) {
	t.Setenv("AGENTBOT_TEST_TOKEN", "token-from-env")
	t.Setenv("AGENTBOT_TEST_SIGNATURE", "sig-from-env")
	t.Setenv("AGENTBOT_TEST_APIKEY", "sk-from-env")
	raw := `transport:
  mode: wsclient
  url: ws://127.0.0.1:3001
  access_token: ${AGENTBOT_TEST_TOKEN}
  signature_secret: ${AGENTBOT_TEST_SIGNATURE}
llm:
  model: m
  api_key: ${AGENTBOT_TEST_APIKEY}
`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Transport.AccessToken == nil || *cfg.Transport.AccessToken != "token-from-env" {
		t.Fatalf("access_token not expanded: %v", cfg.Transport.AccessToken)
	}
	if cfg.Transport.SignatureSecret == nil || *cfg.Transport.SignatureSecret != "sig-from-env" {
		t.Fatalf("signature_secret not expanded: %v", cfg.Transport.SignatureSecret)
	}
	if cfg.LLM.APIKey == nil || *cfg.LLM.APIKey != "sk-from-env" {
		t.Fatalf("api_key not expanded: %v", cfg.LLM.APIKey)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
