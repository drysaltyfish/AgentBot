package transport

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
)

var (
	// ErrUnauthorized 表示入站请求未通过鉴权。
	ErrUnauthorized = errors.New("unauthorized")
	// ErrInboundTokenMissing 表示入站模式没有配置 token（fail-closed）。
	ErrInboundTokenMissing = errors.New("inbound transport requires access_token (fail-closed)")
	// ErrBadAllowlistEntry 表示 IP 白名单条目非法。
	ErrBadAllowlistEntry = errors.New("invalid ip allowlist entry")
)

var defaultAllowlist = []string{"127.0.0.1/32", "::1/128"}

// Auth 承载传输鉴权参数（FEATURES.md F-80）。
//
// 每个 Driver 实例一份，禁止包级共享。请始终以 *Auth 使用。
type Auth struct {
	Token           string
	SignatureSecret string
	IPAllowlist     []string

	once     sync.Once
	nets     []*net.IPNet
	parseErr error
}

// NewAuth 构造 Auth；allowlist 为空时默认只允许回环地址。
func NewAuth(token, signatureSecret string, allowlist []string) *Auth {
	return &Auth{Token: token, SignatureSecret: signatureSecret, IPAllowlist: allowlist}
}

// RequireInboundToken 在入站模式启动前调用：未配置 token 必须直接失败。
func (a *Auth) RequireInboundToken() error {
	if a == nil || a.Token == "" {
		return ErrInboundTokenMissing
	}
	return nil
}

// Validate 在启动期校验白名单条目；解析失败必须让进程起不来。
func (a *Auth) Validate() error {
	a.parse()
	return a.parseErr
}

func (a *Auth) parse() {
	a.once.Do(func() {
		entries := a.IPAllowlist
		if len(entries) == 0 {
			entries = defaultAllowlist
		}
		for _, e := range entries {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			if !strings.Contains(e, "/") {
				ip := net.ParseIP(e)
				if ip == nil {
					a.parseErr = fmt.Errorf("%w: %q", ErrBadAllowlistEntry, e)
					return
				}
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				a.nets = append(a.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
			_, n, err := net.ParseCIDR(e)
			if err != nil {
				a.parseErr = fmt.Errorf("%w: %q: %v", ErrBadAllowlistEntry, e, err)
				return
			}
			a.nets = append(a.nets, n)
		}
	})
}

// AuthorizeURL 把 access_token 追加到出站 URI 上。
func (a *Auth) AuthorizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse transport url: %w", err)
	}
	if a != nil && a.Token != "" {
		q := u.Query()
		q.Set("access_token", a.Token)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// CheckBearer 校验 Authorization 头，兼容 ?access_token= 形态由调用方先行取出。
func (a *Auth) CheckBearer(header string) error {
	if a == nil || a.Token == "" {
		return ErrInboundTokenMissing
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ErrUnauthorized
	}
	return constantTimeEqual(strings.TrimPrefix(header, prefix), a.Token)
}

// CheckQueryToken 校验 URL 查询参数里的 access_token。
func (a *Auth) CheckQueryToken(values url.Values) error {
	if a == nil || a.Token == "" {
		return ErrInboundTokenMissing
	}
	return constantTimeEqual(values.Get("access_token"), a.Token)
}

func constantTimeEqual(got, want string) error {
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

// VerifySignature 校验 X-Signature = "sha1=" + hex(HMAC-SHA1(body))。
func (a *Auth) VerifySignature(body []byte, signature string) error {
	if a == nil || a.SignatureSecret == "" {
		return ErrUnauthorized
	}
	mac := hmac.New(sha1.New, []byte(a.SignatureSecret))
	mac.Write(body)
	want := "sha1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.TrimSpace(signature)), []byte(want)) {
		return ErrUnauthorized
	}
	return nil
}

// AllowIP 判断远端地址是否在白名单内（空名单 = 只允许回环）。
func (a *Auth) AllowIP(remote string) bool {
	a.parse()
	if a.parseErr != nil {
		return false
	}
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	for _, n := range a.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// AuthorizeHTTP 是 HTTP 上报入口的统一判定。
func (a *Auth) AuthorizeHTTP(remote string, body []byte, signature string) error {
	if a == nil {
		return ErrUnauthorized
	}
	if a.SignatureSecret != "" {
		return a.VerifySignature(body, signature)
	}
	if !a.AllowIP(remote) {
		return fmt.Errorf("%w: source %s not in allowlist", ErrUnauthorized, remote)
	}
	return nil
}
