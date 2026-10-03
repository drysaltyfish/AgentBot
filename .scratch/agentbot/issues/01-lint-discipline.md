# 01 · F-73 Lint 纪律

- **Feature**: F-73（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: .golangci.yml, internal/unsafeutil/

## 目标

用 lint 从源头禁掉会在生产上炸掉进程的写法：`log.Fatal`/`os.Exit`/`panic`/`unsafe`/无超时 HTTP。CI 中 lint 必须阻断。

## 交付物

- `.golangci.yml`：`disable-all: true` + 显式白名单（errcheck/govet/staticcheck/gosimple/ineffassign/unused/gocritic/bodyclose/contextcheck/errorlint/exhaustive/gosec/revive/forbidigo/gofmt/goimports）
- forbidigo 禁止：`log.Fatal*`/`os.Exit`/`panic(`（init 期断言除外）/`unsafe.`/`pkg/errors`/`time.Sleep`/`http.Get`/`http.Post`
- 固定目录 `internal/unsafeutil/` 作为唯一 unsafe 白名单目录（注释写明理由）

## 验收

- 故意提交 `log.Fatal` 的代码，CI lint 失败并指出位置
- 故意提交 `http.Get` 的代码，CI lint 失败
- `.golangci.yml` 里 `issues.fix: false`

## 备注（已定决策）

- 错误约定（已定）：包装一律 `fmt.Errorf("...: %w", err)`；哨兵用包级 `var ErrXxx = errors.New(...)` + `errors.Is`；**不引入 `pkg/errors`**

## Comments

### 2026-10-02 · 完成记录

实测：`.golangci.yml` 已落地（v2 格式，`disable-all` + 白名单 + forbidigo）。**注意**：本机未安装 golangci-lint，配置的语法与生效需在 CI 首次运行时确认（Job 已固定版本）。

### 2026-10-03 · 更正与补记（原记录有误，此处以实测为准）

**1) 「需在 CI 首次运行时确认」这一条已经作废，且它掩盖了真问题。**

CI 首次运行确实失败了，原因不是配置语法，而是**固定的 golangci-lint 版本太旧**：
`v2.5.0` 由 go1.25 构建，而 golangci-lint 会拒绝 lint 目标 Go 版本高于自身构建版本的模块
（`can't load config: the Go language version (go1.25) ... lower than the targeted Go version (1.27.1)`）。
已改用 `v2.14.0`（由 go1.27.0 构建），并**在本地安装后实测**：配置合法、首轮报 116 个问题。

**2) 本地跑起来之后，这条 lint 纪律自身有两处是错的：**

- forbidigo 里有一条一刀切禁止 `errors.New`，而 F-73 恰恰要求"哨兵错误集中定义在包级 var"，
  也就是 `var ErrXxx = errors.New(...)`。**该规则禁掉了它自己要的写法**，已删除。
- `revive` 的 `unused-parameter` 对回调/接口实现普遍误报（如 `func(ctx, attempt)`），已显式禁用并注明理由。

**3) 清理了 116 个发现中的全部问题**，其中真实缺陷包括：

- `internal/history` 的字段名 `max` 遮蔽 Go 内建 `max` → 改名 `maxItems`
- 目录/文件权限 0755/0644 → 收紧为 0750/0600（gosec G301/G302）
- `internal/bot` 测试里一处恒真死循环（`len(...) >= 0`）
- `internal/transport` 未使用变量、`llm.DefaultRetryable` 可化简的 if-return 等

其余为 gosec 的定向放行（G404 重试抖动、G505 OneBot 协议规定的 HMAC-SHA1、G304 `--config` 路径），
均已在 `.golangci.yml` 中写明理由。

**4) 验收结论**：CI 已连续三次通过 lint 步骤（含 `-race`、双平台 golden、覆盖率门槛）。
教训："等 CI 确认"不是验收，本地把工具装上再跑一遍才是——上述 116 个问题全都在本地一次暴露。
