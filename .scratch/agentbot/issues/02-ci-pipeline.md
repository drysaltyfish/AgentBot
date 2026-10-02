# 02 · F-78 CI 流水线

- **Feature**: F-78（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 01
- **写域（建议）**: .github/workflows/

## 目标

把纪律变成自动化门禁，保证"本地能过 == CI 能过"。

## 交付物

- `.github/workflows/ci.yml`：push 与 pull_request 触发
- Job 1：`go build ./...` → `go vet ./...` → `golangci-lint run` → `go test -race ./...`
- Job 2：黄金测试在 Linux 与 Windows 两个平台跑
- Job 3：基准冒烟 + 覆盖率（门槛 60% 且不许下降）

## 验收

- 故意引入 race 的代码使 CI 失败
- CI 的 Go 版本与 `go.mod` 的 1.27.1 一致（脚本断言）
- Actions 版本固定到具体 tag/SHA，不用 `master`

## 备注（已定决策）

- Go 版本策略（已定）：锁 1.27.1，CI/`.golangci.yml`/`go.mod` 三者一致
- 不做 Makefile；不使用代码生成；lint 只报不改

## Comments

### 2026-10-02 · 完成记录

实测：`.github/workflows/ci.yml` 已落地，含 go.mod/CI 版本一致性断言、`-race`、双平台黄金测试、覆盖率门槛。本机无 C 编译器，`-race` 只能在 CI（ubuntu）执行。
