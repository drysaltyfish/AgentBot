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
- **F-68 指标暴露**：`ops` 配置分节；`GET /metrics`（Prometheus 文本格式，19 个指标），
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
  文档同步测试已加强：示例配置改用与装载相同的 `KnownFields` 解码，
  **任意层级**的未知键都会在测试里失败（原先只查顶层键）；CI 新增一步，
  对示例配置跑完整的 `--check-config` 并断言输出已脱敏。
- **新指标** `memory_judge_verdicts_total{outcome}`：统计语义判官的判定结果
  （`same` / `different` / `unparsed` / `error`）。加上它是为了把
  "判官解析失败率"从不可观测变成可观测——那是决定 F-31 是否接线的唯一依据（见 ADR-0004）。

### 变更

- `cmd/server` 组合根拆成多文件（详见 README），新增 `observability.go` 与 `admin.go`。
- 配置新增四个分节：`ratelimit`、`toggle`、`audit`、`ops`。
- **`transport.mode: wsserver` / `http` 现在会在启动前被拒绝**。这两种入站驱动
  从未实现（仓库里只有正向 WS 的 `WSClient`），但过去它们能通过校验、
  `--check-config` 返回 0，进程随后带着**空 URL** 打印"已启动"并无限重连——
  与真正的网络故障无法区分。恢复这两种模式时要一并加回"入站必须配 `access_token`"。
- **`--selftest` 现在与 `serve` 共用同一份传输鉴权构造**：它会带上
  `signature_secret` 并执行 `Validate()`。此前自检用的是一个少了密钥、也没校验的拼装，
  于是"服务连得上、自检连不上"，而自检恰恰是排障时最该可信的路径。
- **`--check-config` 打印的是生效值**：此前会把未配置的可选字段打成 `null`，
  让人以为默认值没生效。现在未显式配置的字段按 accessor 的默认值打印，
  只有密钥/人格/提示词这类"确实可以没有"的字段保持为空。
- **沙箱启用时 `http_fetch` 默认被拒**：它声明了 `SandboxRequest{Network: true}`，
  而策略默认禁止网络。需要 `sandbox.network_tools: [http_fetch]` 或 `allow_network: true`；
  启动时会告警说明。不启用沙箱时不受影响。
- **传输连接会重连**：连接失败或读循环结束都按指数退避重试（复用 `retry.Default().Delay`），
  断开时把 readiness 置回 false。此前一次 `Connect` 失败就让该 goroutine 退出——
  进程健康、`/readyz` 仍在报 ready，而机器人永远收不到消息。
- **回复投递合并为一个实现**：正常回复与语义缓存命中此前各写一遍分段+发送+记账。
  发送失败的日志文案统一为 `send failed`（原缓存命中路径为 `send cached reply failed`）。
- **F-05 边界补充**：`RetryCaller` 只重试**幂等动作**（`transport.RetryableActions`）。
  传输层失败无法区分"请求没到"与"到了但回包没回来"，对 `send_msg` 重试会让用户收到两遍。
- **F-31 接线状态**：能力实现完整但**没有消费方**，本轮**不接线**而是先加指标观测；
  决策与反转条件见 [ADR-0004](docs/adr/0004-structured-output-not-wired.md)。
- **F-55 的验收"所有发送路径都经过过滤链"补上了两半**：一条运行时断言
  （用记录**完整请求**的测试替身抓住真正发往平台的请求，证明内容带着过滤链的痕迹，
  含语义缓存命中这条路径），一条**结构性断言**（扫描生产源码，确认除
  `internal/outbound` 之外没有任何包调用发送包装函数）。
  后者防的是"新增一条绕过唯一出口的发送路径"——那种情况下旧测试全绿而内容已绕过过滤。
  注意：规格点名的 `transport.RecordingCaller` **只记录消息 ID、拿不到内容**，
  它无法完成这条验收，故改用记录完整请求的替身（见 HANDOFF 第 18 条）。

### 修复

- 消息装配被生产路径绕开：`conversation.Assembler` 现在是唯一装配点，
  环境消息预算与呈现窗口真正生效，`prefix_hash` 记录的是实际发送的前缀。
- **`superuser: [0]` 不再被当成超管**：校验用的是一份重复扫描，
  与 `access.NewRoles` 的判定不一致，导致 `0` 能通过 fail-closed 关卡。现在两处同源。
- **直连（非 ReAct）路径不再丢记忆**：`DirectAgent` 缺少 `WithMemoryScope`，
  导致记忆召回恒为空；同时它没有上报 memory digest，会产生**假的前缀分叉告警**。
- **`bot.Shutdown` 在没有配置 `inflightWait` 时永远超时**：默认实现退化成了
  "等全部后台 goroutine"，而那些 goroutine 要等第 3 步的 `baseCancel` 才会退出——
  第 2 步必然等到超时。现在默认是空操作。
- **单飞的 mid/post 两半改为成对注册**：只挂 `Rule()` 而漏掉 `Release()` 的后果不是
  "单飞失效"而是**永久失效**（该 key 之后每条消息都被拒）。现在绑在一个 helper 里。
- **F-32 的"provider 实测优先"真正接上了**：`MeasuredCounter` 此前零消费方，
  上下文预算永远停在启发式估算；现在预算与计数器共用同一实例，
  并在每次成功响应后 `Observe`（记的是裁剪后真正发出的那份序列）。
- **路由表顺序在启动时断言**：兜底路由（`record`）一旦排到 `reply` 前面就会吃掉所有消息，
  机器人表现为完全不回复而进程、探针、日志全部正常。
- **入站 pre 钩子顺序在启动时断言**（`inboundPreHookOrder` + `checkPreHookOrder`）。
- **前缀分叉告警不再可能静默失效**：`store` 落库的 relation 字符串与 `llm` 的常量
  是两套独立定义、靠字面量相等匹配；现由跨包词表一致性测试守住。

## 更早

M0 / M1 / M2 / M3（持久化与检索）的逐张 ticket 记录见
[`.scratch/agentbot/issues/`](.scratch/agentbot/issues/) 与
[`.scratch/agentbot/spec.md`](.scratch/agentbot/spec.md)。
