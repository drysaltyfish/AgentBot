# AgentBot

> 跑在 QQ（OneBot 协议）上的自用 AI 机器人：**单二进制 + 同目录配置 + 内嵌 SQLite**，不依赖任何外部服务。

AgentBot 把聊天消息交给大模型，带着长期记忆与工具调用能力回复。它是一份**工程纪律优先**的实现：先立规矩（lint / CI / fake / 优雅关闭），再写业务；每条行为都要在日志或台账里看得见。

---

## 目录

- [这是什么](#这是什么)
- [功能状态](#功能状态)
- [快速开始](#快速开始)
- [命令行](#命令行)
- [配置](#配置)
- [架构](#架构)
- [目录结构](#目录结构)
- [开发](#开发)
- [文档索引](#文档索引)

---

## 这是什么

AgentBot 的设计取向刻意保守，理解这几条就读懂了它的大部分取舍：

| 取向 | 具体含义 |
|---|---|
| **单二进制 + 同目录配置** | 不做 K8s / Helm / Makefile，不引入数据库服务端。部署就是拷一个可执行文件加一份 YAML。 |
| **缓存优先** | 提示词切成「不可变前缀 → 记忆 → 只追加历史 → 当前输入」。前缀逐字节稳定才能命中 provider 的前缀缓存，命中率是**可查询、可回归**的指标，不是感觉。 |
| **行为必须可见** | 关键路径都有结构化日志；token、请求、缓存命中、成本落在台账里可查；失败路径留线索而不是静默降级。 |
| **fail-closed** | 配置校验不过就启动失败；权限表没配好就不放行；持久层打不开就报错退出，绝不悄悄退回内存。 |
| **不依赖既有项目** | 全部自行组装；`参考项目/` 只用于调研，不参与构建，也不进版本控制。 |

它**不是**：通用聊天框架、多租户服务、带 Web UI 的产品。

---

## 功能状态

规范是 [`FEATURES.md`](FEATURES.md)（89 条 Feature）。进度以 [`.scratch/agentbot/spec.md`](.scratch/agentbot/spec.md) 的 ticket 表为准。

### 已完成（85 / 89；"已接入运行时"需另计，见下）

- **M0 骨架**（ticket 01–06）：lint 纪律、CI、手写 Fake、配置校验、结构化日志、优雅关闭。
- **M1 最小闭环**（ticket 07–32）：事件与消息模型、传输 Driver/Caller/echo、传输鉴权、路由注册表与规则、Session、LLM 接口与重试、提示词模板、Agent 契约、动作流解析、权限即提示词、出口过滤链、出站 HTTP 安全。
- **M2 交互与工具**（ticket 33–44）：工具注册表与自描述接口、ReAct 循环、虚拟动作、泛型参数解析、内置安全工具、工具权限与人工审批、临时路由与交互式等待、Agent 接入组合根、记忆落盘与作用域隔离、历史落盘与召回。
- **M3 补记 · 生产可用补齐**：令牌桶限速（F-18）、功能开关（F-19）、审计日志（F-60）、指标暴露（F-68）、健康与就绪探针（F-69）。
- **第三批**：反射状态绑定（F-22）、配置热加载引擎（F-24）、Reflexion（F-36）、Orchestrator-Workers（F-37）、分层记忆（F-49）、混合检索（F-51）、入站内容审查（F-57）、黑名单与防刷（F-58）、密钥管理（F-61）。
  其中 **F-57/F-58 已接入运行时**（入站审查 pre-hook + 审计/指标）；**F-24/F-36/F-37/F-49/F-51 已接线一部分**：F-24 交付了敏感词表热加载，其余见"接线状态"一节。
- **M3 补记 · 第二批**：单飞反并发（F-17）、事件背压队列（F-20）、二值向量检索（F-50）、敏感词引擎 AC 自动机（F-56）、感知哈希去重（F-62）、基准测试补齐（F-77）、接口断言与文档同步（F-79）。
  其中 F-50/F-56/F-62 是库能力：消费方（混合检索 F-51、入站审查 F-57、图片入站链路）在后续批次接线。
- **M3 持久化与检索**（ticket 45–51，F-83~F-89）：SQLite 持久层与版本化迁移、消息归档与中文混合全文检索、会话台账与用量归集、记忆落库与写入决策、记忆遗忘/导出/留存、在途操作持久化、提示词快照与可重放。

### 接线状态（F-79：库已就绪 ≠ 已交付）

以下能力已经实现并有测试，但**尚未（完整）接入 `cmd/server` 运行时**，因此不能算交付。
F-36/F-37 已在本轮接入（LLM Evaluator + `agent.paradigm`），故不在此表：

| 能力 | Feature | 缺什么 |
|---|---|---|
| 分层记忆 / 混合检索 | F-49 / F-51 | 需 SQLite `TierStore`（否则重启丢记忆，是行为退化） |
| 语义缓存 | F-63 | 需 `Vectorize` 与出口过滤，接在 LLM 请求路径 |
| 摘要树 | F-52 | 需注入 `Summarizer`/`Embedder` |
| 分布式追踪 | F-72 | 需入站上下文桥接与出站 HTTP 头传播（且**无 OTLP 导出**，见已知偏离） |
| 流式增量发送 | F-64 | 需接 outbound 发送路径 |
| 配置热加载（其余资产，**部分已接**） | F-24 | 已交付"敏感词表"一项；提示词/开关/限速未接 |
| 成本配额（会话维度，**部分已接**） | F-66 | 已交付全局计量与 `/cost`；会话/用户配额需会话键进入 LLM 调用链 |
| 作用域配置剩余部分（**部分已接**） | F-82 | 已交付人格目录与启动期校验；会话人格持久化与 RouteKey 未接 |

剩余工作的入口、前置条件、风险与已知偏离，见 [HANDOFF.md](HANDOFF.md)。

### 尚未完成（4 / 89）

完整清单见 [里程碑与未完成功能](#里程碑与未完成功能)。

一句话概括：**只剩 4 条未做**——F-07（多账号路由）与 F-27（多供应商路由）在本次目标范围之外，F-23（会话生命周期验收）与 F-65（前缀稳定化验收）留作最后的整体端到端验收。

但"功能写完"不等于"已交付"：见下一节。

---

## 快速开始

### 前置条件

- Go **1.27.1**（`go.mod`、CI、`.golangci.yml` 三处锁定同一版本）
- 一个 OneBot v11 实现（例如 NapCat）暴露 WebSocket
- 一个 OpenAI 兼容的模型端点（默认按 DeepSeek 配置）

### 运行

```sh
git clone git@github.com:drysaltyfish/AgentBot.git
cd AgentBot

# 1. 准备配置
cp config.example.yaml config.yaml
#    至少改三处：llm.api_key（或设置 DEEPSEEK_API_KEY）、transport.url、transport.self_id

# 2. 校验配置（打印脱敏后的生效配置，不启动服务）
go run ./cmd/server --config config.yaml --check-config

# 3. 启动
go run ./cmd/server --config config.yaml
```

### 自检

连上平台后，给自己发一条消息验证整条链路：

```sh
go run ./cmd/server --config config.yaml --selftest <你的QQ号>
```

### 构建

```sh
go build -o bin/agentbot ./cmd/server
```

---

## 命令行

所有子命令都是**一次性维护动作**：执行完打印结果并退出，不会顺带启动服务。

| 参数 | 说明 |
|---|---|
| `--config <path>` | 配置文件路径，默认 `config.yaml` |
| `--check-config` | 只校验配置并打印**脱敏后**的生效配置，然后退出。用于部署前确认解析结果 |
| `--selftest <QQ>` | 连接平台 → `get_login_info` → 给该 QQ 发一条自检消息 → 退出 |
| `--stats` | 打印用量台账：消息总数、请求数、工具调用数、输入/输出/推理 token、前缀缓存命中率、估算成本、花费最高的会话 |
| `--export-memories <path>` | 把**全部**记忆导出为 JSONL 后退出。跨作用域，因此属于维护命令而不是会话内能力 |

---

## 配置

配置是单份 YAML，字段语义见 [`config.example.yaml`](config.example.yaml) 的逐项注释。顶层分节：

| 分节 | 作用 | 要点 |
|---|---|---|
| `transport` | 平台连接 | `mode`/`url`/`self_id`/`access_token`/`signature_secret`/`ip_allowlist`。入站模式未配 token 即启动失败（F-80，fail-closed） |
| `llm` | 模型接入 | `provider`（`echo`/`openai`/`deepseek`）、`model`、`api_key`（支持 `${VAR}` 展开）、`thinking`、`reasoning_effort`、`system_prompt(_file)`、`history_turns`、`ambient_token_budget`、`ambient_max_chars`、`pricing` |
| `store` | 持久层 | `path`、`busy_timeout`。打不开即启动失败，不降级为内存（ADR-0003） |
| `history` | 历史 | `file`（旧 JSONL，用于一次性导入）、`retention`（**存储**保留量，远大于呈现窗口） |
| `agent` | Agent 能力 | `enabled`、`max_iterations`、`step_timeout`、`protocol`（`native`/`auto`，ADR-0001）、`virtual_actions`、`memory*`、`tools`、`approval_*`、`auto_memory`、`memory_judge`、`proactive_memory`、`tool_hint` |
| `behavior` | 回复行为 | 私聊/群聊回复策略、按空行分段发送、连发间隔、最大条数 |
| `prompt` | 提示词 | 模板相关配置（F-33） |
| `policy` | 权限 | 权限表路径等（F-53） |
| `log` | 日志 | `level`、`format`、`components`、`debug_content`、`queue_size` |
| `shutdown` | 关闭 | 优雅关闭超时 |
| `ratelimit` | 令牌桶限速（F-18） | `enabled`（默认关）、单用户/单群的每分钟次数与突发容量；超限事件被整条丢弃 |
| `toggle` | 功能开关（F-19） | `enabled`（默认关）、`default_on`、`file`（落盘后重启保持）；用 `/switch <plugin> on\|off` 管理 |
| `audit` | 审计日志（F-60） | **不可关闭**；`file`/`stdout`/`queue_size`/`content_limit`（用户内容只留前 N 字） |
| `ops` | 指标与探针（F-68/F-69） | `enabled`（默认开）、`addr`（默认 `127.0.0.1:9090`）、`auth_token`、就绪缓存与探针超时 |

关于"未设置 vs 零值"：可选字段一律用 `*T` 指针（决策 6），默认值由 `internal/config` 的访问器解析，调用方不需要自己写 `if p != nil`。

> 安全提示：`config.yaml` 与 `data/` 已在 `.gitignore` 中；`--check-config` 输出的密钥一律脱敏。

---

## 架构

### 消息流

```
OneBot 平台
   │ WebSocket 帧
   ▼
transport.WSClient ──► sink（传输层读循环，**禁止在此调用平台 API**）
   │                        │
   │                        ├─ 会话级临时路由命中？──► 交给 Await 等待者
   │                        ▼
   │                  router.Engine.Dispatch
   │                        │
   │        ┌───────────────┴────────────────┐
   │        ▼                                ▼
   │  reply 规则路由（命中即回复）       Always 兜底路由（只记录）
   │        └───────────────┬────────────────┘
   │                        ▼ 入队（有界、满则丢弃并告警）
   │                  reply worker 池（4 个 goroutine）
   │                        ▼
   │              internal/reply.Pipeline.Handle
   │   自动记忆 → 引用解析 → 记录用户轮次 → agent.Run → 用量台账 → 快照 → 分段发送
   │                        ▼
   │                  agent.ReactAgent（ReAct 循环 + 工具）
   │                        ▼
   └──────────────  outbound.Sender（唯一出口，过滤链 + 审计）
```

要点：

- **所有消息都入队**，不只是被 @ 的。群聊里没被 @ 的内容是模型理解上下文的环境消息，按 token 预算压缩而非丢弃。
- **传输读循环里不做任何 API 调用**。OneBot 的响应只能由同一个读循环读回，在那里 `Call` 必然死锁；引用解析因此放在 worker 里。
- **回复策略是路由规则**，不是 handler 里的分支：路由层就能回答"什么时候回复"。
- **唯一出口**：所有出站消息经 `outbound.Sender`，过滤与审计在一处完成。

### 关键设计决策

| 主题 | 决策 | 记录 |
|---|---|---|
| 工具调用协议 | 原生 `tool_calls` 是唯一执行通道；文本动作解析降级为"抢救通道"（scavenge） | [`docs/adr/0001`](docs/adr/0001-tool-call-protocol.md) |
| 记忆注入位置 | 记忆作为独立消息放在 **system 之后、历史之前**，同时保住 system 段的全局缓存 | [`docs/adr/0002`](docs/adr/0002-memory-injection-position.md) |
| 持久化分层 | 单一内嵌 SQLite；表按**生命周期**分层（消息流 / 会话台账 / 长期记忆 / 在途状态 / 提示词快照）；JSONL 退化为导入导出格式 | [`docs/adr/0003`](docs/adr/0003-persistence-and-data-layering.md) |
| 消息装配 | `internal/conversation.Assembler` 是**唯一装配点**：不可变前缀、记忆位置、呈现窗口、环境消息压缩都归它。agent 不再自己拼消息 | 见下 |

**为什么装配只有一份实现**：曾经 `Assembler` 只有测试在调用，生产由 ReactAgent / DirectAgent 各自拼装。后果是配置并写进日志的 `ambient_token_budget` 与呈现窗口从未生效，日志里的 `prefix_hash` 记的还是一份没被使用的副本。现在 agent 经 `MessageAssembler` 接口注入装配器，并有回归测试断言"实际发出的请求逐条等于装配器输出"。

### 记忆写入决策（F-87）

字符相似度不足以判断语义：「旧的一条」与「新的一条」相似度正好 0.50，却是两件不同的事。因此写入是**混合判据**：

- 完全相同 / 相似度 ≥ 0.90 / < 0.30 → 确定性判定，不调用模型；
- 落在 0.30~0.90 的歧义带 → 用**关闭思考**的模型问一次"是不是同一件事"（结果按文本对缓存）；
- 判官不可用时退回确定性判据，写入照常成功。

### 前缀缓存优先

历史裁剪用**高水位批量滑动**（`trimHistory`），窗口在两次移动之间完全稳定——逐轮滑动会让请求前缀每轮都变，缓存必然失效。存储保留量（`history.retention`，默认 400）远大于呈现窗口（`llm.history_turns`），`recall_history` 才有窗口之外的内容可召回。

成本与命中率不是"日志里看一眼"：`--stats` 可直接查询，`prompt_snapshots` 记录每轮的提示词指纹并区分「记忆变更（预期）」与「前缀意外分歧（告警）」。

---

## 目录结构

```
AgentBot/
├── cmd/server/                组合根：读配置 → 装配 → 启动 → 优雅关闭
│   ├── main.go                CLI 入口、参数与子命令分发
│   ├── serve.go               装配与运行时接线（worker 池、sink、信号、关闭）
│   ├── build.go               LLM / 判官 / Agent 的构造
│   ├── configmap.go           配置 → 运行时类型的映射（发送形态、回复规则、身份）
│   ├── maintenance.go         --stats / --export-memories / --selftest
│   └── runtime.go             适配器、在途恢复、日志渲染辅助
├── internal/
│   ├── agent/                 Agent 契约、ReAct 循环、虚拟动作、记忆接口（F-34~F-40, F-47~F-48）
│   ├── bot/                   生命周期编排：组件注册、在途等待、优雅关闭（F-70）
│   ├── config/                配置 schema / 默认值 / 有效值访问器 / 校验 / 密钥（F-25）
│   ├── conversation/          消息装配：前缀、记忆、窗口、环境压缩（ADR-0002）
│   ├── event/                 事件与消息模型、消息段、消息 ID（F-01~F-03）
│   ├── history/               对话历史接口 + SQLite 实现 + JSONL 导入（F-38, F-84）
│   ├── httpx/                 出站 HTTP 安全客户端（F-59）
│   ├── llm/                   LLM 接口、OpenAI 兼容实现、流式契约、重试、用量与计价（F-26~F-30, F-85）
│   ├── memory/                长期记忆：作用域、混合写入决策、判官（F-47, F-48, F-87）
│   ├── observe/               结构化日志与追踪 ID（F-67）
│   ├── outbound/              出口过滤链与发送（F-55）
│   ├── policy/                权限判定与缓存（F-53, F-54）
│   ├── prompt/                提示词模板引擎（F-33）
│   ├── reply/                 一次回复轮次：自动记忆、引用解析、台账、发送
│   ├── retry/                 退避与重试
│   ├── router/                实例化路由注册表、规则、引擎三阶段钩子（F-08~F-14, F-81）
│   ├── scope/                 会话作用域 ctx key 的唯一所有者
│   ├── session/               Session 与 Manager、临时路由与交互式等待（F-15, F-16, F-21）
│   ├── store/                 SQLite 持久层：迁移、消息、台账、记忆、在途、快照（F-83~F-89）
│   ├── testutil/              手写 Fake（F-76）
│   ├── textsim/               文本相似度
│   ├── tool/                  工具注册表与参数解析（F-41~F-43）
│   ├── tool/builtin/          内置安全工具集（F-44）
│   ├── transport/             WebSocket 客户端、鉴权、Caller 抽象（F-04~F-06, F-80）
│   └── unsafeutil/            `unsafe` 的唯一白名单目录（F-73）
├── prompts/                   人格提示词（私有资产不入库，示例见 prompts/README.md）
├── docs/adr/                  架构决策记录
├── .scratch/agentbot/         规格与本轮 ticket（spec.md + issues/）
├── FEATURES.md                **唯一功能规范**（89 条）
├── DEPENDENCIES.md            依赖准入清单（含许可证核对）
└── config.example.yaml        带注释的示例配置
```

约定：除 `cmd/server` 外全部放 `internal/`；不在装配路径上的包不进仓库。

---

## 开发

### 常用命令

```sh
go build ./...                 # 构建
go vet ./...                   # 静态检查
go test ./...                  # 跑测试
go test -race ./...            # 竞态检测（CI 使用）
go test -run=XXX -bench=. -benchtime=1x ./...   # 基准冒烟
gofmt -l cmd internal          # 格式检查（应无输出）
```

### 测试约定

- **Feature 编号进测试名**：`Test_F30_Backoff`。看到一个测试就能定位它覆盖哪条 Feature。
- **fake 优先**：核心链路单测不依赖网络、不 sleep。`internal/testutil` 提供手写 Fake。
- **契约测试**：流式契约、单动作端到端、多轮工具调用契约（F-75 的 M1/M2 判据）。
- **Golden 测试**：F-74 尚未落地；CI 里已有一条"探测到 golden 用例才断言、没有则显式告知"的守卫，避免假安全感。

### CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) 四个 job：

1. **build-test**：断言 `go.mod` 与 CI 的 Go 版本一致 → build → vet → golangci-lint → `go test -race`；
2. **golden-cross-platform**：Linux + Windows 矩阵，并断言 CI 中拒绝 `-update` 重写 golden；
3. **bench-coverage**：基准冒烟 + 覆盖率（低于 60% 失败）；
4. 覆盖率产物上传。

### Lint

`golangci-lint` v2 配置见 [`.golangci.yml`](.golangci.yml)，启用 errcheck / govet / staticcheck / gocritic / contextcheck / gosec / revive / forbidigo 等。几条硬规则：**库代码禁止 `log.Fatal` / `os.Exit` / `panic` / `time.Sleep` / 裸 `http.Get`**；`unsafe` 只在 `internal/unsafeutil` 白名单；错误一律 `fmt.Errorf("...: %w", err)`（禁 `pkg/errors`）。

---

## 文档索引

| 文档 | 内容 |
|---|---|
| [`FEATURES.md`](FEATURES.md) | 唯一功能规范：89 条 Feature 的价值/规格/边界/验收 |
| [`.scratch/agentbot/spec.md`](.scratch/agentbot/spec.md) | 本轮目标、已定决策、ticket 索引与里程碑 |
| [`.scratch/agentbot/issues/`](.scratch/agentbot/issues/) | 逐张 ticket 的实现记录 |
| [`docs/adr/`](docs/adr/) | 架构决策记录（工具协议 / 记忆位置 / 持久化分层） |
| [`DEPENDENCIES.md`](DEPENDENCIES.md) | 依赖准入清单与许可证核对 |
| [`config.example.yaml`](config.example.yaml) | 逐项注释的配置示例 |
| [`AGENTS.md`](AGENTS.md) | 协作者（人或 AI）的入口约定 |

---

## 里程碑与未完成功能

里程碑规划见 [`FEATURES.md` 附录 A](FEATURES.md)。当前 **M0–M2 全部完成，M3 只完成了持久化子集（F-83~F-89）**。

### A. 明确未实现（M3 计划内，6 条）

| 编号 | 名称 | 优先级 |
|---|---|:--:|
| F-07 | 多账号路由 | P1 |
| F-27 | Provider 注册与多供应商路由 | P1 |
| F-29 | 流式工具调用聚合 | P1 |
| F-31 | 结构化输出 | P1 |
| F-64 | 流式增量发送 | P1 |
| F-74 | 黄金测试 | P1 |

### B. 部分实现（M3 计划内，5 条；已有代码但未按 Feature 完整验收）

| 编号 | 名称 | 现状 |
|---|---|---|
| F-23 | 会话生命周期与回收 | 已有 TTL、容量上限、淘汰计数与回收（`session.WithTTL` / `Evicted` / `Reclaim`）并有测试 |
| F-32 | Token 计量与上下文预算 | 已有用量计量与环境消息预算/呈现窗口；`Budget.Fit` 与 `Summarize` 未做（分歧点 G5 未裁定） |
| F-66 | 成本统计与配额 | 成本/token 统计随 F-85 落地并可查（`--stats`）；**配额**未做 |
| F-75 | 契约测试 | M1/M2 判据（流式契约、单动作端到端、多轮工具调用）已满足；其余契约未覆盖 |
| F-82 | 作用域配置与人格路由键 | 有 `config.Persona` 与 `Session.Persona`/`SetPersona`；人格路由键的完整接线未完成 |

### C. M4 · 增强（6 条，均未开始）

| 编号 | 名称 | 优先级 | 备注 |
|---|---|:--:|---|
| F-46 | 工具执行沙箱 | P2 | |
| F-52 | 摘要树（RAPTOR 式） | P2 | |
| F-63 | 语义缓存 | P2 | |
| F-65 | 提示词前缀稳定化 | P2 | **工程上实质已达成**（前缀哈希、缓存优先布局、快照回归），但未按该 Feature 单独验收 |
| F-71 | 管理命令 | P2 | CLI 维护命令已有（`--stats` / `--export-memories`）；聊天内管理命令未做 |
| F-72 | 分布式追踪 | P2 | 已有 trace ID 贯穿日志；未接 OpenTelemetry 等 |

> 上表由 `.scratch/agentbot/spec.md` 的 ticket 表（权威进度）配合代码检索核对得出。如果某个 Feature 其实已经落地但没进 ticket 表，请以代码为准并更新 spec。

---

## 许可

MIT。第三方依赖及其许可证见 [`DEPENDENCIES.md`](DEPENDENCIES.md)。
