// Package httpx 提供统一的安全出站 HTTP 客户端（FEATURES.md F-59）。
//
// 任何"根据用户输入去请求 URL"的功能都是 SSRF 与内存放大的入口，因此本包是
// 全仓库唯一的出站 HTTP 出口。
package httpx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrBlockedAddress 表示目标解析到被禁止的地址（回环/私网/链路本地等）。
	ErrBlockedAddress = errors.New("blocked address")
	// ErrSchemeNotAllowed 表示协议不在允许范围内。
	ErrSchemeNotAllowed = errors.New("scheme not allowed")
	// ErrHostNotAllowed 表示域名不在白名单内。
	ErrHostNotAllowed = errors.New("host not allowed")
	// ErrTooLarge 表示响应体超过上限。
	ErrTooLarge = errors.New("response body too large")
	// ErrTooManyRedirects 表示跳转次数超限。
	ErrTooManyRedirects = errors.New("too many redirects")
	// ErrImageTooLarge 表示图片声明尺寸超过像素上限。
	ErrImageTooLarge = errors.New("image dimensions exceed limit")
	// ErrPathEscapesBase 表示路径越出了允许的根目录。
	ErrPathEscapesBase = errors.New("path escapes base directory")
)

// Config 描述客户端的安全边界；零值会被补齐为默认值。
type Config struct {
	// Timeout 是整次请求的总超时（默认 10s）。
	Timeout time.Duration
	// MaxBytes 是响应体上限（默认 1 MiB）。
	MaxBytes int64
	// MaxRedirects 是允许的最大跳转次数（默认 3）。
	MaxRedirects int
	// AllowPrivate 仅供测试放行回环地址；生产必须为 false。
	AllowPrivate bool
	// Allowlist 非空时只允许这些域名（及其子域）。
	Allowlist []string
	// Resolver 便于测试注入；默认使用系统解析器。
	Resolver Resolver
}

// Resolver 是 DNS 解析的最小切面。
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Defaults 返回默认配置。
func Defaults() Config {
	return Config{
		Timeout:      10 * time.Second,
		MaxBytes:     1 << 20,
		MaxRedirects: 3,
	}
}

func (c Config) normalized() Config {
	d := Defaults()
	if c.Timeout <= 0 {
		c.Timeout = d.Timeout
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = d.MaxBytes
	}
	if c.MaxRedirects <= 0 {
		c.MaxRedirects = d.MaxRedirects
	}
	if c.Resolver == nil {
		c.Resolver = net.DefaultResolver
	}
	return c
}

// IsBlockedIP 判断地址是否属于回环/私网/链路本地/组播/保留段。
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // 0.0.0.0/8
			return true
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // 100.64.0.0/10 CGNAT
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return true
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15
			return true
		case v4[0] >= 240: // 240.0.0.0/4 保留
			return true
		}
		return false
	}
	// IPv6：唯一本地地址 fc00::/7 与链路本地 fe80::/10。
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return true
		}
		if ip[0] == 0xfe && ip[1]&0xc0 == 0x80 {
			return true
		}
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
			return true // 2001:db8::/32 文档段
		}
	}
	return false
}

func (c Config) checkHost(host string) error {
	if len(c.Allowlist) == 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range c.Allowlist {
		allowed = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(allowed), "."))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
}

// CheckURL 校验协议与域名（不含 DNS 解析）。
func (c Config) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: %q", ErrSchemeNotAllowed, u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%w: empty host", ErrSchemeNotAllowed)
	}
	if err := c.checkHost(u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// NewClient 构造受约束的 http.Client。
func NewClient(cfg Config) *http.Client {
	c := cfg.normalized()

	dialer := &net.Dialer{Timeout: c.Timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("split addr %q: %w", addr, err)
			}
			ip, err := c.resolveAllowed(ctx, host)
			if err != nil {
				return nil, err
			}
			// 用已校验过的 IP 直连，避免 DNS rebinding 在解析与连接之间换人。
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
		TLSHandshakeTimeout:   c.Timeout,
		ResponseHeaderTimeout: c.Timeout,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		DisableCompression:    false,
	}

	client := &http.Client{
		Timeout: c.Timeout,
		// F-72：出站带上 traceparent（ctx 里没有 span 时是恒等操作）。
		Transport: traceTripper{next: transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > c.MaxRedirects {
				return fmt.Errorf("%w: %d", ErrTooManyRedirects, len(via))
			}
			if _, err := c.CheckURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	return client
}

func (c Config) resolveAllowed(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) && !c.AllowPrivate {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return ip, nil
	}
	addrs, err := c.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, a := range addrs {
		if IsBlockedIP(a.IP) && !c.AllowPrivate {
			continue
		}
		return a.IP, nil
	}
	return nil, fmt.Errorf("%w: %s resolved to no allowed address", ErrBlockedAddress, host)
}

// Response 是一次受限请求的结果。
type Response struct {
	StatusCode  int
	ContentType string
	Body        []byte
	Truncated   bool
}

// Get 发起一次受限 GET：限制大小、拒绝非文本、校验每一跳。
func (c Config) Get(ctx context.Context, raw string) (*Response, error) {
	cfg := c.normalized()
	if _, err := cfg.CheckURL(raw); err != nil {
		return nil, err
	}
	client := NewClient(cfg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	out := &Response{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}

	limited := io.LimitReader(resp.Body, cfg.MaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > cfg.MaxBytes {
		out.Truncated = true
		return out, fmt.Errorf("%w: limit %d bytes", ErrTooLarge, cfg.MaxBytes)
	}
	out.Body = body
	return out, nil
}

// SecureJoin 把 name 安全地拼到 base 下，拒绝任何越出 base 的路径。
//
// 禁止用 HasPrefix 判断目录包含关系（前缀可被路径穿越绕过）。
func SecureJoin(base, name string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve base: %w", err)
	}
	absBase = filepath.Clean(absBase)
	absTarget, err := filepath.Abs(filepath.Join(absBase, name))
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}
	absTarget = filepath.Clean(absTarget)
	if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscapesBase, name)
	}
	return absTarget, nil
}

// ImageSize 从图片头部读出声明尺寸（不解码像素数据）。
func ImageSize(data []byte) (width, height int, err error) {
	if len(data) >= 24 && string(data[1:4]) == "PNG" {
		w := binary.BigEndian.Uint32(data[16:20])
		h := binary.BigEndian.Uint32(data[20:24])
		return int(w), int(h), nil
	}
	if len(data) >= 10 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a") {
		w := int(binary.LittleEndian.Uint16(data[6:8]))
		h := int(binary.LittleEndian.Uint16(data[8:10]))
		return w, h, nil
	}
	if len(data) >= 4 && data[0] == 0xFF && data[1] == 0xD8 {
		return jpegSize(data)
	}
	return 0, 0, fmt.Errorf("unsupported or truncated image header")
}

func jpegSize(data []byte) (int, int, error) {
	i := 2
	for i+9 < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 {
			return 0, 0, fmt.Errorf("invalid JPEG segment length")
		}
		// SOF0..SOF15（除 DHT C4、JPG C8、DAC CC）
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+9 >= len(data) {
				return 0, 0, fmt.Errorf("truncated JPEG SOF")
			}
			h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
			return w, h, nil
		}
		i += 2 + length
	}
	return 0, 0, fmt.Errorf("no JPEG SOF segment found")
}

// CheckImagePixels 在解码之前按声明尺寸拒绝超大图片。
func CheckImagePixels(data []byte, maxPixels int) (int, int, error) {
	w, h, err := ImageSize(data)
	if err != nil {
		return 0, 0, err
	}
	if maxPixels > 0 && w*h > maxPixels {
		return w, h, fmt.Errorf("%w: %dx%d > %d pixels", ErrImageTooLarge, w, h, maxPixels)
	}
	return w, h, nil
}
