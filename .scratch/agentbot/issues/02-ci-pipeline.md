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

### 2026-10-03 · 补记（CI 已实际运行，验收闭环）

**1) `-race` 验收已完成**：CI 的 `go test -race ./...` 已多次跑过并全部 `ok`，
本机无 C 编译器这一限制不再是缺口。

**2) go.mod 与 CI 的 Go 版本一致性断言**已在真实运行中生效（1.27.1）。

**3) Actions 固定版本**：`actions/checkout@v5`、`actions/setup-go@v6`、`actions/upload-artifact@v4`，
`golangci-lint` 固定 `v2.14.0`（由 go1.27.0 构建；旧固定值 `v2.5.0` 因构建 Go 版本低于模块目标版本而直接拒绝运行）。

**4) 修掉一处假门禁（重要）**：golden job 里"断言 `-update` 在 CI 中被拒绝"这一步，
原先写成 `go test ./... -run Golden -update | grep -q "CI"` 而后失败——
它有两个独立缺陷：

- **判据是反的**：输出里出现 `CI` 反而判为失败；
- **完全空转**：当时没有任何 golden 用例，`-update` 只会触发"未知 flag"报错，
  输出不含 `CI`，于是这一步无论对错都通过——比没有这一步更糟，因为它给出假安全感。

已重写为：先探测哪些包真的含 golden 用例（注意 `-list` 不受 `-run` 约束，
写成 `-run Golden -list '.*'` 会把全部 244 个测试都列出来），只对这些包断言。
三条路径均已本地实测：无 golden 用例 → 显式 notice 并放行；
助手拒绝 `-update` → 通过；助手接受 `-update` → `::error::` 失败。

**5) 已知非阻断告警**：`actions/upload-artifact@v4` 被强制跑在 Node 24（官方弃用告警）；
`ubuntu-latest` 将于 2026-10-19 迁移到 Ubuntu 26。两者当前均不影响结果。
