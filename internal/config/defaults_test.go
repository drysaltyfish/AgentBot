package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/conversation"
	"gopkg.in/yaml.v3"
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

// Test_ZeroValuesAreRejectedWhereEffectiveWouldOverrideThem 钉住一处"校验与默认值各说各话"。
//
// 一批字段用 orPositive 解析：<=0 会被当作"未设置"换成默认值。若校验只拒绝负值，
// 那么 0 既能通过校验、又会被运行时静默替换——运维在 --check-config 里看到 0，
// 实际跑的却是 20 / 0.95。凡是 accessor 无法表示的值，校验就必须拒绝，
// 否则 sections.go 那句"禁止用 0 即未设置"就只是注释。
func Test_ZeroValuesAreRejectedWhereEffectiveWouldOverrideThem(t *testing.T) {
	t.Parallel()
	z := func(v int) *int { return &v }
	f := func(v float64) *float64 { return &v }

	base := func() *Config {
		c := Default()
		c.LLM.Model = "m"
		c.Transport.Mode = "wsclient"
		c.Transport.URL = "ws://127.0.0.1:1"
		return c
	}

	d := base() // 只为取 accessor 兜底后的值，用于说明为什么必须拒绝 0
	cases := []struct {
		path string
		mut  func(*Config)
		// override 是"如果放行 0，运行时实际会用的值"，用于说明为什么必须拒绝。
		override any
	}{
		{"llm.history_turns", func(c *Config) { c.LLM.HistoryTurns = z(0) }, d.LLM.EffectiveHistoryTurns()},
		{"ratelimit.user_per_minute", func(c *Config) { c.RateLimit.UserPerMinute = z(0) }, d.RateLimit.EffectiveUserPerMinute()},
		{"ratelimit.user_burst", func(c *Config) { c.RateLimit.UserBurst = z(0) }, d.RateLimit.EffectiveUserBurst()},
		{"ratelimit.group_per_minute", func(c *Config) { c.RateLimit.GroupPerMinute = z(0) }, d.RateLimit.EffectiveGroupPerMinute()},
		{"ratelimit.group_burst", func(c *Config) { c.RateLimit.GroupBurst = z(0) }, d.RateLimit.EffectiveGroupBurst()},
		{"semcache.threshold", func(c *Config) { c.Semcache.Threshold = f(0) }, d.Semcache.EffectiveThreshold()},
	}

	for _, tc := range cases {
		cfg := base()
		tc.mut(cfg)
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("%s=0 必须被拒绝：accessor 会把它换成 %v，放行等于静默改配置", tc.path, tc.override)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || !ve.Has(tc.path) {
			t.Fatalf("%s 的报错应指向该字段，实际: %v", tc.path, err)
		}
	}

	// 正值照常通过（别把校验改得过严）。
	ok := base()
	ok.LLM.HistoryTurns = z(1)
	ok.RateLimit.UserPerMinute = z(1)
	ok.RateLimit.UserBurst = z(1)
	ok.RateLimit.GroupPerMinute = z(1)
	ok.RateLimit.GroupBurst = z(1)
	ok.Semcache.Threshold = f(0.5)
	if err := ok.Validate(); err != nil {
		t.Fatalf("正当配置不应被拒绝: %v", err)
	}
}

// Test_EffectiveDoesNotMutateTheReceiver 保证 Effective() 只是"算一份生效值"，
// 不会把默认值写回调用方手里的配置对象。
func Test_EffectiveDoesNotMutateTheReceiver(t *testing.T) {
	t.Parallel()
	c := Default()
	before, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_ = c.Effective()
	after, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Effective() 改动了接收者")
	}
}

// Test_EffectiveConfigPrintsNoMisleadingNulls 是 --check-config 的守卫。
//
// 默认值有两套机制：defaults.go 装载时填一部分，effective.go 的 accessor 读取时兜底。
// 只填了一半的话，--check-config 会把有默认值的字段打印成 null，
// 运维看到的数字与真正跑的不是同一个。这条测试要求：
// 生效配置里出现的每一个 null，都必须属于"没有本身就是配置"的那几类。
//
// 新增一个"有默认值但忘了在 Effective() 里解析"的指针字段时，这条测试会失败。
func Test_EffectiveConfigPrintsNoMisleadingNulls(t *testing.T) {
	t.Parallel()
	out, err := Default().Effective().RedactedYAML()
	if err != nil {
		t.Fatalf("RedactedYAML: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var nulls []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch typed := v.(type) {
		case map[string]any:
			for k, vv := range typed {
				walk(prefix+"."+k, vv)
			}
		case map[any]any:
			for k, vv := range typed {
				walk(prefix+"."+fmt.Sprint(k), vv)
			}
		case nil:
			nulls = append(nulls, strings.TrimPrefix(prefix, "."))
		}
	}
	walk("", doc)

	// 允许为 null 的字段：nil 本身就是"未配置"这一配置。
	allowed := map[string]bool{
		// 密钥：没配就是没配。
		"transport.access_token":     true,
		"transport.signature_secret": true,
		"llm.api_key":                true,
		"llm.api_key_env":            true,
		"llm.api_key_file":           true,
		// 三态：nil 表示"不下发该字段"，与 false 不同。
		"llm.thinking": true,
		// 回退到内置资产：nil 表示用代码里的默认提示词/人格/基础范式。
		"llm.system_prompt":      true,
		"llm.system_prompt_file": true,
		"prompt.persona":         true,
		"agent.paradigm":         true,
		// 可选开关：nil 与空串等价，都是"不启用"。
		"ops.auth_token":              true,
		"moderation.mask_replacement": true,
		// 计价与空转字段：nil 与 0 等价（全 0 = 不统计成本）。
		"llm.pricing.input_per_million":     true,
		"llm.pricing.output_per_million":    true,
		"llm.pricing.cache_hit_per_million": true,
		// llm.max_iterations 是已标注的空转字段（没有消费方），nil 不隐藏任何生效值。
		"llm.max_iterations": true,
	}

	for _, path := range nulls {
		if !allowed[path] {
			t.Errorf("%s 在生效配置里是 null，但它有默认值——"+
				"请在 Effective() 里解析它，或（若 null 本身就有意义）把它加进这条测试的允许清单", path)
		}
	}
}

// Test_EffectiveResolvesSpotValues 抽查若干字段：不只要求"不是 null"，
// 还要求解析出来的值确实等于 accessor 给出的生效值。
func Test_EffectiveResolvesSpotValues(t *testing.T) {
	t.Parallel()
	c := Default()
	e := c.Effective()

	if got, want := e.LLM.AmbientTokenBudget, conversation.DefaultAmbientTokenBudget; got == nil || *got != want {
		t.Fatalf("ambient_token_budget: actual=%v expected=%d（默认值由 internal/conversation 拥有）", got, want)
	}
	if got := e.Agent.Reflexion.MaxReflections; got == nil || *got != c.Agent.EffectiveMaxReflections() {
		t.Fatalf("reflexion.max_reflections 未解析成生效值")
	}
	if got := e.Prompt.PersonasDir; got == nil || *got != c.Prompt.EffectivePersonasDir() {
		t.Fatalf("prompt.personas_dir 未解析成生效值")
	}
	if got := e.Store.BusyTimeout; got == nil {
		t.Fatalf("store.busy_timeout 未解析——它的默认值由 internal/store 拥有，见 defaults.go 的成对常量")
	}
	// 显式配置的值必须原样保留，不能被默认值顶掉。
	explicit := Default()
	split := Duration{D: 0}
	explicit.Behavior.SplitDelay = &split
	if got := explicit.Effective().Behavior.SplitDelay; got == nil || got.D != 0 {
		t.Fatalf("显式 split_delay=0 必须保留")
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
