package httpx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_F59_BlockedAddressClassification(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"::1", "fc00::1", "fd00::1", "fe80::1", "0.0.0.0", "224.0.0.1",
		"100.64.0.1", "198.18.0.1", "240.0.0.1", "2001:db8::1",
	}
	for _, s := range blocked {
		if !IsBlockedIP(net.ParseIP(s)) {
			t.Fatalf("IsBlockedIP(%s): actual=false expected=true", s)
		}
	}
	if IsBlockedIP(nil) != true {
		t.Fatalf("IsBlockedIP(nil): actual=false expected=true")
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1::1"}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Fatalf("IsBlockedIP(%s): actual=true expected=false", s)
		}
	}
}

func Test_F59_CheckURLRejectsSchemeAndDisallowedHost(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	for _, bad := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://x/1", "http://"} {
		if _, err := cfg.CheckURL(bad); err == nil {
			t.Fatalf("CheckURL(%q): actual=nil expected=error", bad)
		}
	}
	if _, err := cfg.CheckURL("https://example.com/x"); err != nil {
		t.Fatalf("CheckURL(https): actual=%v expected=nil", err)
	}

	allow := Defaults()
	allow.Allowlist = []string{"example.com"}
	if _, err := allow.CheckURL("https://api.example.com/x"); err != nil {
		t.Fatalf("subdomain of allowlisted host: actual=%v expected=nil", err)
	}
	if _, err := allow.CheckURL("https://evil.example.net/x"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("non-allowlisted host: actual=%v expected=ErrHostNotAllowed", err)
	}
}

type fakeResolver struct {
	ip  net.IP
	err error
}

func (f fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []net.IPAddr{{IP: f.ip}}, nil
}

func Test_F59_DialRejectsPrivateTargets(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.Timeout = 2 * time.Second

	// 直接写私网 IP
	if _, err := cfg.Get(context.Background(), "http://127.0.0.1:1/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("direct loopback: actual=%v expected=ErrBlockedAddress", err)
	}
	if _, err := cfg.Get(context.Background(), "http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("metadata service: actual=%v expected=ErrBlockedAddress", err)
	}
	if _, err := cfg.Get(context.Background(), "http://[::1]:1/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("IPv6 loopback: actual=%v expected=ErrBlockedAddress", err)
	}

	// 域名解析到私网（SSRF 常见形态）
	evil := Defaults()
	evil.Timeout = 2 * time.Second
	evil.Resolver = fakeResolver{ip: net.ParseIP("10.1.2.3")}
	if _, err := evil.Get(context.Background(), "http://internal.example/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("domain resolving to private IP: actual=%v expected=ErrBlockedAddress", err)
	}
}

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string, Config) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	cfg := Defaults()
	cfg.Timeout = 3 * time.Second
	cfg.AllowPrivate = true
	cfg.Resolver = fakeResolver{ip: net.ParseIP("127.0.0.1")}
	return srv, port, cfg
}

func Test_F59_HappyPathWithAllowPrivate(t *testing.T) {
	t.Parallel()
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello"))
	})
	resp, err := cfg.Get(context.Background(), "http://local.test:"+port+"/x")
	if err != nil {
		t.Fatalf("Get: actual=%v expected=nil", err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "hello" {
		t.Fatalf("response: actual=(%d,%q)", resp.StatusCode, resp.Body)
	}
	if !strings.HasPrefix(resp.ContentType, "text/plain") {
		t.Fatalf("content type: actual=%q", resp.ContentType)
	}
}

func Test_F59_ResponseBodyIsLimited(t *testing.T) {
	t.Parallel()
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64*1024)
		for i := 0; i < 48; i++ { // 3 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	cfg.MaxBytes = 1 << 20
	_, err := cfg.Get(context.Background(), "http://local.test:"+port+"/big")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("3 MiB response: actual=%v expected=ErrTooLarge", err)
	}
}

func Test_F59_RedirectToDisallowedHostIsRejected(t *testing.T) {
	t.Parallel()
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.example/", http.StatusFound)
	})
	cfg.Allowlist = []string{"local.test"}
	_, err := cfg.Get(context.Background(), "http://local.test:"+port+"/redirect")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect to disallowed host: actual=%v expected=ErrHostNotAllowed", err)
	}
}

func Test_F59_TooManyRedirectsIsRejected(t *testing.T) {
	t.Parallel()
	_, port, cfg := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	cfg.MaxRedirects = 2
	_, err := cfg.Get(context.Background(), "http://local.test:"+port+"/loop")
	if err == nil {
		t.Fatalf("redirect loop: actual=nil expected=error")
	}
	if !errors.Is(err, ErrTooManyRedirects) && !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect loop error: actual=%v", err)
	}
}

func Test_F59_SecureJoinRejectsTraversal(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ok, err := SecureJoin(base, filepath.Join("sub", "a.txt"))
	if err != nil {
		t.Fatalf("legit path: actual=%v expected=nil", err)
	}
	if !strings.HasPrefix(ok, base) {
		t.Fatalf("resolved path outside base: actual=%s base=%s", ok, base)
	}
	for _, bad := range []string{"../outside.txt", "../../etc/passwd", "sub/../../escape.txt"} {
		if _, err := SecureJoin(base, bad); !errors.Is(err, ErrPathEscapesBase) {
			t.Fatalf("SecureJoin(%q): actual=%v expected=ErrPathEscapesBase", bad, err)
		}
	}
}

func Test_F59_ImageDeclaredSizeIsRejectedBeforeDecoding(t *testing.T) {
	t.Parallel()
	png := make([]byte, 24)
	copy(png[1:4], "PNG")
	binary.BigEndian.PutUint32(png[16:20], 100000)
	binary.BigEndian.PutUint32(png[20:24], 100000)

	w, h, err := ImageSize(png)
	if err != nil || w != 100000 || h != 100000 {
		t.Fatalf("ImageSize(png): actual=(%d,%d,%v)", w, h, err)
	}
	if _, _, err := CheckImagePixels(png, 8192*8192); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("100000x100000: actual=%v expected=ErrImageTooLarge", err)
	}
	if len(png) != 24 {
		t.Fatalf("test must not allocate pixel data: actual=%d bytes", len(png))
	}

	gif := make([]byte, 10)
	copy(gif[0:6], "GIF89a")
	binary.LittleEndian.PutUint16(gif[6:8], 640)
	binary.LittleEndian.PutUint16(gif[8:10], 480)
	if w, h, err := ImageSize(gif); err != nil || w != 640 || h != 480 {
		t.Fatalf("ImageSize(gif): actual=(%d,%d,%v)", w, h, err)
	}

	jpeg := make([]byte, 14)
	jpeg[0], jpeg[1] = 0xFF, 0xD8
	jpeg[2], jpeg[3] = 0xFF, 0xC0
	binary.BigEndian.PutUint16(jpeg[4:6], 17)
	jpeg[6] = 8
	binary.BigEndian.PutUint16(jpeg[7:9], 300)  // height
	binary.BigEndian.PutUint16(jpeg[9:11], 400) // width
	if w, h, err := ImageSize(jpeg); err != nil || w != 400 || h != 300 {
		t.Fatalf("ImageSize(jpeg): actual=(%d,%d,%v)", w, h, err)
	}

	if _, _, err := ImageSize([]byte("not an image")); err == nil {
		t.Fatalf("unsupported header: actual=nil expected=error")
	}
	_ = fmt.Sprint()
}
