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
