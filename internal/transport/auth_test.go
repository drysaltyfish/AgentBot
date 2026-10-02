package transport

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func Test_F80_InboundTokenIsFailClosed(t *testing.T) {
	t.Parallel()
	empty := NewAuth("", "", nil)
	if err := empty.RequireInboundToken(); !errors.Is(err, ErrInboundTokenMissing) {
		t.Fatalf("empty token: actual=%v expected=ErrInboundTokenMissing", err)
	}
	ok := NewAuth("secret", "", nil)
	if err := ok.RequireInboundToken(); err != nil {
		t.Fatalf("configured token: actual=%v expected=nil", err)
	}
	if err := empty.CheckBearer("Bearer "); !errors.Is(err, ErrInboundTokenMissing) {
		t.Fatalf("CheckBearer without configured token: actual=%v expected=ErrInboundTokenMissing", err)
	}
}

func Test_F80_AuthorizeURLAppendsToken(t *testing.T) {
	t.Parallel()
	a := NewAuth("tok-123", "", nil)
	got, err := a.AuthorizeURL("ws://127.0.0.1:3001/onebot/v11/ws")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Query().Get("access_token") != "tok-123" {
		t.Fatalf("access_token: actual=%q expected=%q", u.Query().Get("access_token"), "tok-123")
	}
	noToken, err := NewAuth("", "", nil).AuthorizeURL("ws://x/y")
	if err != nil || strings.Contains(noToken, "access_token") {
		t.Fatalf("empty token should not be appended: actual=%q err=%v", noToken, err)
	}
}

func Test_F80_BearerChecks(t *testing.T) {
	t.Parallel()
	a := NewAuth("tok-123", "", nil)
	if err := a.CheckBearer("Bearer tok-123"); err != nil {
		t.Fatalf("valid bearer: actual=%v expected=nil", err)
	}
	for _, h := range []string{"tok-123", "Bearer tok-124", "Bearer ", "bearer tok-123"} {
		if err := a.CheckBearer(h); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("CheckBearer(%q): actual=%v expected=ErrUnauthorized", h, err)
		}
	}
	vals := url.Values{"access_token": {"tok-123"}}
	if err := a.CheckQueryToken(vals); err != nil {
		t.Fatalf("query token: actual=%v expected=nil", err)
	}
}

func Test_F80_SignatureVerification(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t"
	body := []byte(`{"post_type":"message"}`)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	good := "sha1=" + hex.EncodeToString(mac.Sum(nil))

	a := NewAuth("tok", secret, nil)
	if err := a.VerifySignature(body, good); err != nil {
		t.Fatalf("valid signature: actual=%v expected=nil", err)
	}
	if err := a.VerifySignature(body, "sha1=deadbeef"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad signature: actual=%v expected=ErrUnauthorized", err)
	}
	if err := a.VerifySignature([]byte("tampered"), good); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("tampered body: actual=%v expected=ErrUnauthorized", err)
	}
	noSecret := NewAuth("tok", "", nil)
	if err := noSecret.VerifySignature(body, good); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no secret configured: actual=%v expected=ErrUnauthorized", err)
	}
}

func Test_F80_AllowIPDefaultsToLoopbackAndSupportsCIDR(t *testing.T) {
	t.Parallel()
	def := NewAuth("t", "", nil)
	if !def.AllowIP("127.0.0.1:5000") {
		t.Fatalf("default allowlist should include IPv4 loopback")
	}
	if !def.AllowIP("[::1]:5000") {
		t.Fatalf("default allowlist should include IPv6 loopback")
	}
	if def.AllowIP("10.0.0.1:5000") {
		t.Fatalf("default allowlist must not include LAN addresses")
	}

	custom := NewAuth("t", "", []string{"10.0.0.0/8", "192.168.1.5"})
	if !custom.AllowIP("10.1.2.3:9") {
		t.Fatalf("CIDR entry not honoured")
	}
	if !custom.AllowIP("192.168.1.5") {
		t.Fatalf("bare IP entry not honoured")
	}
	if custom.AllowIP("11.0.0.1:9") {
		t.Fatalf("address outside allowlist was allowed")
	}
}

func Test_F80_BadAllowlistEntryFailsAtStartup(t *testing.T) {
	t.Parallel()
	a := NewAuth("t", "", []string{"not-an-ip"})
	if err := a.Validate(); !errors.Is(err, ErrBadAllowlistEntry) {
		t.Fatalf("Validate: actual=%v expected=ErrBadAllowlistEntry", err)
	}
	if a.AllowIP("127.0.0.1") {
		t.Fatalf("allowlist with a bad entry must fail closed")
	}
}

func Test_F80_AuthorizeHTTPPrefersSignatureThenIP(t *testing.T) {
	t.Parallel()
	body := []byte("{}")
	a := NewAuth("t", "sec", []string{"10.0.0.0/8"})
	if err := a.AuthorizeHTTP("10.0.0.1:1", body, "sha1=bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad signature from allowed IP must still be rejected: actual=%v", err)
	}
	ipOnly := NewAuth("t", "", []string{"10.0.0.0/8"})
	if err := ipOnly.AuthorizeHTTP("10.0.0.1:1", body, ""); err != nil {
		t.Fatalf("allowed IP without secret: actual=%v expected=nil", err)
	}
	if err := ipOnly.AuthorizeHTTP("8.8.8.8:1", body, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disallowed IP: actual=%v expected=ErrUnauthorized", err)
	}
}
