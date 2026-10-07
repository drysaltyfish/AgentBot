package main

import (
	"errors"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// Test_BuildTransportAuthCarriesEveryField 钉住"鉴权配置只拼一次"。
//
// serve 与 --selftest 曾经各拼一个：自检那份**少了签名密钥、也没跑 Validate()**。
// 于是配了签名校验的部署会出现"服务连得上、自检连不上"——而自检恰恰是排障时
// 最该可信的那条路径。这类分歧只在特定配置下暴露，平常看起来完全正常。
func Test_BuildTransportAuthCarriesEveryField(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Transport.AccessToken = ptr("tok")
	cfg.Transport.SignatureSecret = ptr("secret")
	cfg.Transport.IPAllowlist = []string{"10.0.0.0/8"}

	auth, err := buildTransportAuth(cfg)
	if err != nil {
		t.Fatalf("buildTransportAuth: %v", err)
	}
	if auth.Token != "tok" {
		t.Fatalf("access_token 没带进去: %q", auth.Token)
	}
	if auth.SignatureSecret != "secret" {
		t.Fatalf("签名密钥没带进去，自检会与线上不一致: %q", auth.SignatureSecret)
	}
	if len(auth.IPAllowlist) != 1 || auth.IPAllowlist[0] != "10.0.0.0/8" {
		t.Fatalf("IP 白名单没带进去: %v", auth.IPAllowlist)
	}
}

// Test_BuildTransportAuthValidates 非法白名单必须在构造期就被拒绝。
//
// 这条同时证明 Validate() 真的被调用了：自检那条路径以前根本没调它，
// 于是"配置写错了"要等到实际连接时才以难以理解的形式暴露。
func Test_BuildTransportAuthValidates(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Transport.IPAllowlist = []string{"not-an-ip"}

	if _, err := buildTransportAuth(cfg); !errors.Is(err, transport.ErrBadAllowlistEntry) {
		t.Fatalf("非法白名单必须让构造失败，实际: %v", err)
	}
}

// Test_BuildTransportAuthAllowsEmptyAllowlist 未配置白名单不是错误（默认只允许回环）。
func Test_BuildTransportAuthAllowsEmptyAllowlist(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if _, err := buildTransportAuth(cfg); err != nil {
		t.Fatalf("空白名单应走默认值而不是报错: %v", err)
	}
}
