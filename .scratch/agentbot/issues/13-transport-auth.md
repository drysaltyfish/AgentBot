# 13 · F-80 传输鉴权

- **Feature**: F-80（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 10
- **写域（建议）**: internal/transport/

## 目标

反向 WS 与 HTTP 上报都是入站入口；没有鉴权，任何人都能投递伪造事件驱动 LLM 与工具。

## 交付物

- `Auth{Token, IPAllowlist, SignatureSecret}`，每 Driver 实例一份，禁止包级共享
- `wsclient` 出站：URI query 携带 `access_token`
- `wsserver` 入站：校验 `Authorization: Bearer`，Token 未配置则**启动失败**（fail-closed）
- `http` 入站：校验 `X-Signature = "sha1=" + hex(HMAC-SHA1(body))`，否则只接受 `IPAllowlist`（默认 `127.0.0.1`）

## 验收

- 未配置 Token 启动 `wsserver` → 启动失败且退出码非 0
- 错误 Token 连接被拒（401）且全程零事件投递
- 错误签名上报被拒、正确签名通过
- 密钥比较使用 `hmac.Equal`/`subtle.ConstantTimeCompare`，禁止 `==`

## 备注（已定决策）

- `wsserver`/`http` 实现体在 M3；M1 交付鉴权逻辑 + `wsclient` 侧 + 单测
- 密钥日志脱敏（F-61）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/transport/auth.go` + 单测；未配置 token 时 `RequireInboundToken` 返回 `ErrInboundTokenMissing`（fail-closed，且 `config.Validate` 在启动期已拦截，见 ticket 04）；`AuthorizeURL` 追加 access_token；Bearer/query token 校验；HMAC-SHA1 `X-Signature` 校验（含 body 篡改）；IP 白名单支持 CIDR 与裸 IP、空名单默认只允许回环（IPv4+IPv6）、坏条目启动期报错且 fail-closed；`AuthorizeHTTP` 同时配置签名与白名单时签名优先。密钥比较全部走 `subtle.ConstantTimeCompare` / `hmac.Equal`。
