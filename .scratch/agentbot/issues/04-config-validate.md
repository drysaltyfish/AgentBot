# 04 · F-25 配置校验与 fail-fast

- **Feature**: F-25（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/config/

## 目标

配置写错必须在启动时一次性报全部错误，而不是在第一条用户消息时崩溃。

## 交付物

- 配置项元信息：类型、是否必填、默认值、取值范围、是否敏感
- `Validate() error` 收集**全部**错误后一次性返回
- `--check-config`：只校验并打印最终生效配置（脱敏），不启动服务

## 验收

- 缺失必填项时输出全部缺失项并退出码 1
- 显式设置为零值被接受（不被误判为未设置）
- 敏感项日志只显示前 4 位 + 长度

## 备注（已定决策）

- 未设置 vs 零值（已定）：**一律用 `*T` 指针**，不引入 `Option[T]`
- 配置格式（已定）：YAML，解析用 `gopkg.in/yaml.v3`
- 依赖准入见 `DEPENDENCIES.md`

## Comments

### 2026-10-02 · 完成记录

实测：`internal/config` + 单测 8 项全过；`go run ./cmd/server --config config.example.yaml --check-config` 输出脱敏配置、退出码 0；缺失必填项时**一次列出全部问题**（llm.model + transport.url）并退出码 1；入站模式无 token 时 fail-closed 退出码 1。
