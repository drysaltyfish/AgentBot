# 32 · F-59 出站 HTTP 安全

- **Feature**: F-59（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/httpx/

## 目标

任何"根据用户输入去请求 URL"的功能都是 SSRF 与内存放大的入口。

## 交付物

- 统一 HTTP client：总超时 10s、DialContext 超时、TLSHandshakeTimeout、连接池上限、`CheckRedirect` 最多 3 跳且每跳重新校验
- 响应体 `io.LimitReader`（1 MiB），超限断开并报错
- SSRF 防护：拒绝全部 IPv4/IPv6 私网与特殊地址，**对最终连接的 IP 校验**

## 验收

- 请求 `127.0.0.1`、`10.0.0.1`、`169.254.169.254`、`::1`、重定向到私网 → 全部被拒绝
- 3 MiB 响应被截断并报错（不 OOM）
- 声明尺寸 100000×100000 的图片被拒绝且未分配大内存
- 本地文件路径用 `filepath.Clean` + 绝对前缀校验（禁止 `HasPrefix`）

## 备注（已定决策）

- 所有出站 HTTP 必须经本包，禁止 `http.Get`/`http.Post`（F-73 已用 lint 强制）
- 解压炸弹防护：限制解压后大小

## Comments

### 2026-10-02 · 完成记录

实测：`internal/httpx/client.go` + `client_test.go`（10 个测试全过）。`IsBlockedIP` 覆盖 IPv4 回环/私网/链路本地/CGNAT/保留段与 IPv6 `::1`、`fc00::/7`、`fe80::/10`、`2001:db8::/32`；直接请求 `127.0.0.1`/`169.254.169.254`/`[::1]` 全部返回 `ErrBlockedAddress`；**域名解析到私网**（注入 fake resolver 返回 10.1.2.3）同样被拒——连接使用已校验的 IP，杜绝解析与连接之间的 DNS rebinding；3 MiB 响应返回 `ErrTooLarge`；重定向到非白名单域名返回 `ErrHostNotAllowed`，重定向环返回 `ErrTooManyRedirects`；`SecureJoin` 拒绝 `../`、`../../etc/passwd`、`sub/../../escape.txt`（用 Clean+绝对前缀，不用 HasPrefix）；PNG/GIF/JPEG 头部解析声明尺寸，100000×100000 的 24 字节头部即被 `ErrImageTooLarge` 拒绝，**全程未分配像素数据**。
