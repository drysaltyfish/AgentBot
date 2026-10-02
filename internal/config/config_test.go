package config

import (
	"errors"
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

func Test_F25_InboundAuthIsFailClosed(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"wsserver", "http"} {
		cfg := Default()
		cfg.Transport.Mode = mode
		cfg.LLM.Model = "m"
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("mode=%s without access_token: actual=nil expected=error", mode)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || !ve.Has("transport.access_token") {
			t.Fatalf("mode=%s: actual=%v expected problem transport.access_token", mode, err)
		}
	}

	cfg := Default()
	cfg.Transport.Mode = "wsserver"
	cfg.LLM.Model = "m"
	empty := ""
	cfg.Transport.AccessToken = &empty
	if err := cfg.Validate(); err == nil {
		t.Fatalf("mode=wsserver with empty token: actual=nil expected=error")
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
