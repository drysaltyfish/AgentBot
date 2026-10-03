# Changelog

本项目是**自用应用 + 可复用内核**（见 `FEATURES.md` 决策 1），不承诺公开 API 稳定性。
这里记录的是**行为或对外契约的变化**（配置项、命令行、指标名、规范条目），
纯内部重构不逐条记录。

格式：按时间倒序；每条注明涉及的 Feature 编号。

## 未发布

### 新增能力（M3 生产可用）

- **F-18 令牌桶限速**：`ratelimit` 配置分节（默认关闭）。超限事件会被**整条丢弃**
  （连"只记录"的兜底路由也不执行），这是刻意取舍：不允许刷屏产生任何 LLM 调用。
- **F-19 功能开关**：`toggle` 配置分节（默认关闭）；`/switch <plugin> on|off`
  限 owner/admin，按群生效，落盘后重启保持。
- **F-20 背压队列**：事件分发改为有界队列 + worker 池，队列满按 `DropNewest` 丢弃并计数，
  读循环不再被处理速度拖住。
- **F-60 审计日志**：`audit` 配置分节。审计**不可关闭**；异步有界队列，队列满只丢审计并计数，
  绝不影响主流程；用户内容按 `content_limit` 脱敏截断。
- **F-68 指标暴露**：`ops` 配置分节；`GET /metrics`（Prometheus 文本格式，15 个指标），
  指标在 `internal/metrics` 的目录里**集中定义**。
- **F-69 健康与就绪探针**：`GET /healthz`（只看进程）、`GET /readyz`（存储/传输/provider，
  结果缓存 10s，任一失败返回 503 并列出原因）。默认监听 `127.0.0.1:9090`，可配 `auth_token`。
- **F-17 单飞中间件**：同 key 的并发事件只放行一个，其余拒绝；占位在 `post` 释放（panic 也释放）。
- **F-50 二值向量检索**、**F-56 敏感词引擎（AC 自动机）**、**F-62 感知哈希图片去重**：
  新增 `internal/vector` / `internal/textguard` / `internal/imagehash`。
- **F-77 基准测试**：补齐 `BenchmarkBind` / `BenchmarkRenderPrompt` /
  `BenchmarkPolicyAllow` / `BenchmarkParseMessage`（另有既有的
  `BenchmarkRouteMatch` / `BenchmarkHammingSearch` / `BenchmarkMatchLargeDict`）；
  路由匹配基准带明确预算并在测试里校验。
- **F-79 接口断言与文档同步**：实现文件里补编译期接口断言；
  新增两条文档同步测试（示例配置的键必须存在于 schema；指标名必须能在规范里 grep 到）。

### 变更

- `cmd/server` 组合根拆成多文件（详见 README），新增 `observability.go` 与 `admin.go`。
- 配置新增四个分节：`ratelimit`、`toggle`、`audit`、`ops`。

### 修复

- 消息装配被生产路径绕开：`conversation.Assembler` 现在是唯一装配点，
  环境消息预算与呈现窗口真正生效，`prefix_hash` 记录的是实际发送的前缀。

## 更早

M0 / M1 / M2 / M3（持久化与检索）的逐张 ticket 记录见
[`.scratch/agentbot/issues/`](.scratch/agentbot/issues/) 与
[`.scratch/agentbot/spec.md`](.scratch/agentbot/spec.md)。
