# AgentBot Features

> **文档性质**：AgentBot 的功能规格说明书（Feature Spec）。它描述**要做成什么样**，不描述怎么调研来的。
> **前提**：AgentBot **不依赖**任何既有项目，全部自行组装；每条 Feature 都是可独立实现、可独立验收的功能单元。
> **命名**：Feature 编号 `F-xx`，一经分配不再变动，便于在 issue / commit / 测试名中引用（例如 `Test_F30_Backoff`）。

---

## 0. 使用说明

### 0.1 优先级定义

| 优先级 | 含义 | 判定标准 |
|---|---|---|
| **P0** | 首版必须有 | 缺了它首版不成立：要么跑不通完整链路（收到事件 → 调用 LLM → 回消息），要么违反工程纪律（lint / CI / fake / 优雅关闭） |
| **P1** | 生产可用必需 | 缺了它能在测试环境跑、但不能对真实用户开放 |
| **P2** | 增强 / 可选 | 有了更好，没有不影响正确性 |

### 0.2 条目的字段含义

| 字段 | 含义 |
|---|---|
| **价值** | 这个功能解决什么问题，不做会怎样 |
| **规格** | 可实现的确定性描述：接口、字段、默认值、行为 |
| **边界** | 异常/超时/并发/上限等必须明确处理的场景 |
| **验收** | 可写成自动化测试的判据 |
| **易错点** | 实践中容易踩的坑，实现时必须避开 |

### 0.3 全部 Feature 速览

| 编号 | 名称 | 优先级 | 模块 |
|---|---|:--:|---|
| F-01 | 双轨事件模型 | P0 | 运行时内核 |
| F-02 | 通用消息 ID | P0 | 运行时内核 |
| F-03 | 消息段与消息链 | P0 | 运行时内核 |
| F-04 | 传输抽象 Driver | P0 | 运行时内核 |
| F-05 | 调用抽象 Caller | P0 | 运行时内核 |
| F-06 | 请求-响应关联（echo） | P0 | 运行时内核 |
| F-07 | 多账号路由 | P1 | 运行时内核 |
| F-08 | 实例化路由注册表 | P0 | 路由与调度 |
| F-09 | 稳定优先级排序 | P0 | 路由与调度 |
| F-10 | Rule / Handler 分离 | P0 | 路由与调度 |
| F-11 | 事件上下文与 State | P0 | 路由与调度 |
| F-12 | 热路径快照匹配 | P0 | 路由与调度 |
| F-13 | 三段中间件钩子 | P0 | 路由与调度 |
| F-14 | 内置规则库 | P0 | 路由与调度 |
| F-15 | 一次性 / 临时路由 | P0 | 路由与调度 |
| F-16 | 交互式等待（Await/Stream） | P0 | 路由与调度 |
| F-17 | 单飞（反并发）中间件 | P1 | 路由与调度 |
| F-18 | 令牌桶限速中间件 | P1 | 路由与调度 |
| F-19 | 功能开关中间件 | P1 | 路由与调度 |
| F-20 | 背压队列 | P2 | 路由与调度 |
| F-21 | Session 与 Manager | P0 | 会话与状态 |
| F-22 | 状态注入（反射绑定） | P1 | 会话与状态 |
| F-23 | 会话生命周期与回收 | P1 | 会话与状态 |
| F-24 | 配置热加载 | P1 | 会话与状态 |
| F-25 | 配置校验与 fail-fast | P0 | 会话与状态 |
| F-26 | 统一 LLM 接口 | P0 | LLM 接入 |
| F-27 | Provider 注册与多供应商路由 | P1 | LLM 接入 |
| F-28 | 流式契约 | P0 | LLM 接入 |
| F-29 | 流式工具调用聚合 | P1 | LLM 接入 |
| F-30 | 重试与退避 | P0 | LLM 接入 |
| F-31 | 结构化输出 | P1 | LLM 接入 |
| F-32 | Token 计量与上下文预算 | P1 | LLM 接入 |
| F-33 | 提示词模板引擎 | P0 | LLM 接入 |
| F-34 | 统一 Agent 契约 | P0 | Agent 能力 |
| F-35 | ReAct 循环 | P0 | Agent 能力 |
| F-36 | Reflexion 自我反思 | P2 | Agent 能力 |
| F-37 | Orchestrator-Workers | P2 | Agent 能力 |
| F-38 | 对话历史管理 | P0 | Agent 能力 |
| F-39 | 动作流解析 | P0 | Agent 能力 |
| F-40 | 虚拟动作闭环 | P1 | Agent 能力 |
| F-41 | 工具注册表 | P0 | 工具系统 |
| F-42 | 自描述工具接口 | P0 | 工具系统 |
| F-43 | 泛型参数解析 | P1 | 工具系统 |
| F-44 | 内置安全工具集 | P1 | 工具系统 |
| F-45 | 工具权限与人工审批 | P1 | 工具系统 |
| F-46 | 工具执行沙箱 | P2 | 工具系统 |
| F-47 | 记忆抽象 | P0 | 记忆与知识 |
| F-48 | 书签式长期记忆 | P1 | 记忆与知识 |
| F-49 | 分层记忆 | P2 | 记忆与知识 |
| F-50 | 二值向量检索 | P1 | 记忆与知识 |
| F-51 | 混合检索 | P2 | 记忆与知识 |
| F-52 | 摘要树（RAPTOR 式） | P2 | 记忆与知识 |
| F-53 | 权限即提示词 | P0 | 安全与合规 |
| F-54 | 权限判定缓存 | P0 | 安全与合规 |
| F-55 | 统一出口过滤链 | P0 | 安全与合规 |
| F-56 | 敏感词引擎（AC 自动机） | P1 | 安全与合规 |
| F-57 | 入站内容审查 | P1 | 安全与合规 |
| F-58 | 黑名单与防刷 | P1 | 安全与合规 |
| F-59 | 出站 HTTP 安全 | P0 | 安全与合规 |
| F-60 | 审计日志 | P1 | 安全与合规 |
| F-61 | 密钥管理 | P1 | 安全与合规 |
| F-62 | 感知哈希图片去重 | P2 | 降本增效 |
| F-63 | 语义缓存 | P2 | 降本增效 |
| F-64 | 流式增量发送 | P1 | 降本增效 |
| F-65 | 提示词前缀稳定化 | P2 | 降本增效 |
| F-66 | 成本统计与配额 | P1 | 降本增效 |
| F-67 | 结构化日志与追踪 | P0 | 可观测性 |
| F-68 | 指标暴露 | P1 | 可观测性 |
| F-69 | 健康与就绪探针 | P1 | 可观测性 |
| F-70 | 优雅关闭 | P0 | 可观测性 |
| F-71 | 管理命令 | P2 | 可观测性 |
| F-72 | 分布式追踪 | P2 | 可观测性 |
| F-73 | Lint 纪律 | P0 | 工程化 |
| F-74 | 黄金测试 | P1 | 工程化 |
| F-75 | 契约测试 | P0 | 工程化 |
| F-76 | 手写 Fake | P0 | 工程化 |
| F-77 | 基准测试 | P1 | 工程化 |
| F-78 | CI 流水线 | P0 | 工程化 |
| F-79 | 接口断言与文档同步 | P1 | 工程化 |
| F-80 | 传输鉴权 | P0 | 运行时内核 |
| F-81 | 命令参数解析 | P1 | 路由与调度 |
| F-82 | 作用域配置与人格路由键 | P1 | 会话与状态 |
| F-83 | 嵌入式持久层与版本化迁移 | P0 | 持久化与检索 |
| F-84 | 消息归档与全文检索 | P0 | 持久化与检索 |
| F-85 | 会话台账与用量归集 | P1 | 持久化与检索 |
| F-86 | 在途操作的持久化（可恢复的等待与审批） | P2 | 持久化与检索 |
| F-87 | 记忆持久化与写入决策 | P0 | 持久化与检索 |
| F-88 | 记忆的遗忘、导出与留存 | P1 | 持久化与检索 |
| F-89 | 提示词快照与可重放 | P1 | 持久化与检索 |

**合计 89 条**：P0 42 条、P1 34 条、P2 13 条。

### 0.4 依赖准入

- 只接受 **MIT / ISC / BSD-2-Clause / BSD-3-Clause / Apache-2.0** 许可的第三方模块；引入前必须核对许可证原文并登记到 `DEPENDENCIES.md`（已核对清单见该文件）。
- 标准库优先：能用标准库实现的能力不引入依赖。
- 已批准的依赖（其余一律需要先改本节并说明理由）：

| 用途 | 模块 | 版本 | 许可证（已核对） | 引入里程碑 |
|---|---|---|---|---|
| WebSocket 客户端（F-04） | `github.com/coder/websocket` | v1.8.15 | ISC | M1 |
| YAML 解析（F-25 / F-53 / F-82） | `gopkg.in/yaml.v3` | v3.0.1 | MIT + Apache-2.0 | M1 |
| goroutine 泄漏检测（F-70，仅测试依赖） | `go.uber.org/goleak` | v1.3.0 | MIT | M0 |
| SQLite（F-50 / F-83~F-89） | `modernc.org/sqlite` | v1.60.1 | BSD-3-Clause | M3 |

- `golangci-lint` 是工具链二进制，**不进** `go.mod`。
- 禁止引入 GPL / AGPL / SSPL 或来源不明许可的模块。

---

## 1. 运行时内核（事件与传输）

### F-01 双轨事件模型 · P0

**价值**：不同平台（QQ 群、私聊、频道、其他 IM）的事件字段差异极大。若为每个平台定义一套 struct，业务代码会被迫写大量分支；若只保留归一化字段，扩展能力又会丢失。双轨模型让"业务用归一字段、扩展用原始包"同时成立。

**规格**
- `Event` 结构体分三部分：
  1. **归一字段**：`Kind`（事件大类：`message`/`notice`/`request`/`meta`）、`Sub`（细分：`group`/`private`/`guild`/`poke`…）、`SubSub`（三级细分）。
  2. **通用字段**：`SelfID`、`UserID`、`GroupID`、`MessageID`、`Time`、`Sender`。
- **原始包**：`Raw json.RawMessage`，完整保存平台上报的 JSON（大小上限见下）。
- 派生规则：`Kind` 由 `post_type` 映射；`Sub` 由 `message_type`/`notice_type`/`request_type` 映射；`message_sent` 归一为 `message`，使同一条规则可同时命中两者。
- 提供 `func (e *Event) Get(path string) (any, bool)`，用 JSON Path（如 `sender.role`）从 `Raw` 中按需取值，避免为每个扩展字段加 struct 字段。
- `Raw` 只解析一次并缓存：构造时完成归一化解析，`Get` 复用该缓存，之后只读共享。

**边界**
- `Raw` 为空或非法 JSON 时不得 panic：`Get` 返回 `(nil, false)`，并在 `Event` 上记录一次解码告警。
- `Raw` 大小上限（默认 1 MiB），超过则截断并置 `RawTruncated=true`。
- `Event` 不含任何指针到可变共享缓冲的字段（禁止零拷贝字符串指向 socket 缓冲）。

**验收**
- 给定 6 种平台样例 JSON，`Kind`/`Sub` 映射结果符合表驱动期望。
- `message_sent` 样例的 `Kind` 等于 `message`。
- `Get("sender.card")` 能取到值，且对不存在路径返回 `false` 而不 panic。

**易错点**
- 不要用 `unsafe` 把字节切片直接转字符串指向网络缓冲——缓冲可能被复用。必须拷贝或使用 `json.RawMessage`（会拷贝）。

---

### F-02 通用消息 ID · P0

**价值**：QQ 的消息 ID 是 int64，频道/其他平台是字符串。若统一成 string，下游要频繁转换；若统一成 int64，字符串 ID 会丢失。双表示 ID 让两种平台共用一套下游接口。

**规格**
- `type ID struct { num int64; raw string }`，不可直接构造，只通过构造函数：
  - `IDFromInt64(v int64) ID`
- `IDFromString(s string) ID` —— 先尝试 `strconv.ParseInt`；失败则 `num = int64(crc64.ISO(s))`，若 `num <= 0xffff_ffff` 则把高位段置 1（保证伪造 ID 不落入真实数字 ID 的取值区间），`raw = s`。
- 取值：`Int64() int64`、`String() string`（原始串优先）。
- `MarshalJSON`：若 `raw` 可解析为整数或为空 → 输出 JSON number；否则输出 JSON string。
- `UnmarshalJSON` 同时接受 number 与 string。
- `IsZero()`、`Equal(other ID) bool`（`raw` 非空时比 `raw`，否则比 `num`）。

**边界**
- 空字符串 → `IsZero() == true`。
- 构造必须幂等：`IDFromString(x).String() == x`（当 x 非空）。

**验收**
- 表驱动：`"123"` → JSON `123`；`"abc"` → JSON `"abc"`；往返一致。
- 同一字符串两次构造得到相等 ID。

---

### F-03 消息段与消息链 · P0

**价值**：一条消息是"文本 + 图片 + @ + 表情 + 回复"的有序组合。用字符串拼接会丢失结构，也无法安全转义。

**规格**
- `type Segment struct { Type string; Data map[string]string }`；`type Message []Segment`。
- 类型常量：`text`/`image`/`record`/`video`/`at`/`face`/`reply`/`node`/`markdown` 等。
- 构造器：`Text(s)`、`Image(file)`、`At(qq)`、`Reply(id)`…每个构造器只填必要字段。
- 解析：`ParseMessage(raw json.RawMessage) (Message, error)`，同时支持 **数组形态**（onebot11 标准）与 **CQ 码字符串形态**。
- 序列化：`(Message) Marshal() json.RawMessage`；`(Message) PlainText() string` 抽取纯文本（用于关键词/正则匹配）。
- 转义：段内文本的 `&`/`[`/`]` 必须转义，`CQ 码` 形态额外转义逗号。
- 打印友好：`(Segment) String()` 对 base64 图片只输出长度+哈希前缀，避免日志爆炸。

**边界**
- 未知 `type` 必须原样保留（不得丢弃），以便透传。
- 空消息 → 空 Message，`PlainText() == ""`，不 panic。
- CQ 码字符串解析遇非法转义时跳过该段并记录，不整体失败。

**验收**
- 数组形态与 CQ 码形态解析出**相等**的 Message。
- 解析 → 序列化 → 再解析，结果不变（往返幂等）。
- 表驱动覆盖：空、纯文本、多段、含 at、含 image、含未知类型、含转义字符。

---

### F-04 传输抽象 Driver · P0

**价值**：机器人要能接正向 WebSocket、反向 WebSocket、HTTP 三种上报方式。若把这些写死在主循环里，换协议就要改核心。

**规格**
- `type Driver interface { Connect(ctx context.Context) error; Listen(ctx context.Context, sink func(raw []byte, caller Caller)) error }`
- 内置实现：`wsclient`（正向 WS）、`wsserver`（反向 WS，多连接）、`http`（HTTP 上报）。
- `Connect` **必须返回 error**（与"连不上只打日志"的写法不同），由上层决定重试策略。
- `Listen` 收到 ctx 取消时须**立即返回**并关闭底层连接，不得永久阻塞。
- 连接失败的重试策略外置：`RetryDriver{next, backoff}` 装饰器，默认 1s 起、最长 30s、带 jitter。
- URI 支持 `ws://`、`wss://`、`http://`、`ws+unix://`（unix socket）。

**边界**
- `Listen` 返回后不得再调用 `sink`（避免向已关闭的会话投递）。
- 同一 Driver 重复 `Connect` 必须幂等或返回明确错误，不得静默建立第二条连接。
- 读循环 panic 必须被 recover 并转成 error 返回，不得杀死进程。

**验收**
- 用一个内存管道 mock 传输：Connect 成功 → 投递 3 条事件 → cancel ctx → Listen 在 100ms 内返回。
- Connect 失败时返回非 nil error。

**易错点**
- 不要用 `log.Fatal`/`os.Exit` 处理连接失败；不要在库内无限重连且不给退出信号。

---

### F-05 调用抽象 Caller · P0

**价值**：业务代码需要"发消息/踢人/禁言"等能力，但这些是平台 API。抽象成 Caller 后可被装饰（审计、Mock、限流），也可在单测中替换。

**规格**
- `type Request struct { Action string; Params map[string]any; Echo uint64 }`
- `type Response struct { Status string; Data json.RawMessage; Message string; Wording string; RetCode int64; Echo uint64 }`
- `type Caller interface { Call(ctx context.Context, req Request) (Response, error) }`
- `Response.OK() bool` = `RetCode == 0`。
- 语义化封装（可选糖）：`SendGroupMsg`、`SendPrivateMsg`、`DeleteMsg`、`SetGroupBan`…全部基于 `Call`。
- **Caller 必须可装饰**：`type Middleware func(Caller) Caller`；内置 `RecordingCaller`（记录发出的消息 ID）、`RateLimitedCaller`、`RetryCaller`。

**边界**
- `ctx` 超时必须取消底层请求；返回 error 且 Response 为零值。
- `Params` 为 nil 时按空对象发送，不能 panic。
- 未知 Action 不预校验（平台可能扩展），但 `RecordingCaller` 需容忍 `Data` 为空。

**验收**
- 装饰两层后调用仍能正确穿透并返回原始 Response。
- ctx 超时时 Call 在超时时间内返回 error。

---

### F-06 请求-响应关联（echo）· P0

**价值**：WebSocket 是异步的，但业务代码需要"发一个 API 调用并同步拿到结果"。用自增 echo + 等待表把它变成同步调用。

**规格**
- 每个 Driver 实例持有：`seq atomic.Uint64` 与 `pending sync.Map[uint64, chan Response]`。
- `Call`：`echo := seq.Add(1)` → 建 `chan Response`（容量 1）→ 存入 pending → 写 socket（**写必须加锁**，WebSocket 写非并发安全）→ `select { case r := <-ch: …; case <-ctx.Done(): … }`。
- 收到带 echo 的响应：从 pending 取出并 `close` channel。
- 收到不带 echo 的帧：视为事件，投递给 `Listener`。
- 收到心跳：丢弃（可计数）。

**边界**
- **必须**在 `defer` 中从 pending 删除条目，否则 ctx 取消会泄漏表项。
- 对端回包 echo 但无人等待（超时后）→ 丢弃并计数告警，不得 panic。
- channel 关闭后再写入会 panic：约定"谁删除谁关闭"，且发送方只做非阻塞发送 `select { case ch <- r: default: }`。
- 写 socket 失败时必须从 pending 移除并返回 error。

**验收**
- 并发 100 个 Call，乱序回包，全部能正确配对（每个都拿到自己的 echo 对应响应）。
- ctx 超时后 pending 表项数为 0（无泄漏）。

**易错点**
- WS 写不加锁 → 数据竞争/帧交错；超时后不清理 pending → 内存泄漏。

---

### F-07 多账号路由 · P1

**价值**：一个进程可能同时接入多个机器人账号（多 selfID），事件与调用必须按账号隔离。

**规格**
- `type Registry struct { mu sync.RWMutex; bySelf map[int64]Caller }`。
- `Register(selfID int64, c Caller)`、`Get(selfID int64) (Caller, bool)`、`Range(fn)`。
- 每个 `Event` 携带 `SelfID`；处理时用 `Registry.Get(e.SelfID)` 取得对应 Caller 注入 `Context`。
- 握手：Driver 连接成功后调用平台 `get_login_info` 取得 selfID 再注册。

**边界**
- selfID 为 0（握手失败）时不得注册；该连接的事件全部丢弃并告警。
- 重复注册同一 selfID：覆盖并告警（不 panic）。

**验收**
- 注册 3 个账号，各自事件只路由到自己的 Caller。

---

### F-80 传输鉴权 · P0

**价值**：反向 WebSocket 与 HTTP 上报都是**入站**入口。没有鉴权，任何人都能向机器人投递伪造事件并驱动 LLM 与工具调用——这是整个系统最大的一张敞开的口子。

**规格**
- `type Auth struct { Token string; IPAllowlist []string; SignatureSecret string }`，由配置注入（F-25），每个 Driver 实例一份，禁止包级共享。
- `wsclient`（出站）：URI query 携带 `access_token`。
- `wsserver`（入站）：校验 `Authorization: Bearer <token>`，兼容 `?access_token=`；**Token 未配置时启动失败**（fail-closed）。
- `http`（入站）：配置了签名密钥时校验 `X-Signature = "sha1=" + hex(HMAC-SHA1(body))`；未配置时只接受 `IPAllowlist` 内的来源（默认 `127.0.0.1`）。
- 鉴权失败：返回 401/403、计入指标、按配置静默丢弃；**不得**把失败请求投递给路由层。
- 所有密钥/签名比较必须用 `hmac.Equal` 或 `subtle.ConstantTimeCompare`，**禁止** `==`（防时序侧信道）。

**边界**
- "未配置 Token"与"显式配置为空串"都必须启动失败，二者不得被当成一件事含糊处理。
- 反向 WS 的多连接场景下每个连接独立鉴权；鉴权失败的连接不得进入连接集合。
- `IPAllowlist` 支持 CIDR；条目解析失败在启动期报错。
- 密钥在日志与错误信息中一律脱敏（F-61）。

**验收**
- 未配置 Token 启动 `wsserver` → 启动失败且退出码非 0。
- 携带错误 Token 的连接被拒绝（401），且全程不产生任何事件投递（用 FakeDriver 计数断言）。
- 错误签名的 HTTP 上报被拒绝；正确签名通过。
- 代码评审/测试能证明比较使用 `hmac.Equal`。

---

## 2. 路由与调度

### F-08 实例化路由注册表 · P0

**价值**：插件式框架的核心：任意模块注册"当事件满足 X 时执行 Y"。必须是**实例**而非全局变量，否则无法在一个进程内跑多实例、也无法并行测试。

**规格**
- `type Router struct { mu sync.RWMutex; routes []*Route; epoch uint64 }`
- `type Route`：**字段全部私有**，防止外部绕过 Router 记账（重排序、重名告警）直接改写状态。
  配置走链式方法：`Named`/`Priority`/`Once`/`Block`/`Break`/`Expire`/`Handle`/`UseRules`/`UsePre`；
  读面提供 `Kind`/`Name`/`Level`/`IsOnce`/`IsBlocked`/`SkipsPost`/`Rules`/`Handlers`/`PreRules` 等副本访问器。
- 注册 API 返回 `*Route` 以便链式配置：`r.On("message", rules...).Priority(10).Handle(h)`。
- **禁止包级全局注册表**；`Router` 由 `Bot` 持有，通过依赖注入传递。
- 便捷触发器与通用触发器并存：`OnMessage`/`OnNotice`/`OnRequest`/`OnCommand`/`OnPrefix`/`OnSuffix`/`OnRegex`/`OnKeyword`/`OnFullMatch`/`OnAtMe`。
- 路由自省：`Routes() []RouteInfo`（名称、类型、优先级、是否 Once），供 `/help` 与管理命令展示。

**边界**
- 注册过程中并发匹配必须安全（RWMutex + 快照，见 F-12）。
- `Name` 重复不报错但告警（便于插件重载）。

**验收**
- 两个 `Bot` 实例各自注册不同路由，互不干扰。
- 并发注册 1000 条 + 并发匹配不触发 race（`-race` 通过）。

---

### F-09 稳定优先级排序 · P0

**价值**：同优先级路由的执行顺序必须可预测，否则插件行为会随机化。

**规格**
- 按 `Priority` 升序（数值小者先执行）；同优先级**保持注册顺序**（`sort.SliceStable`）。
- 每次增删/改优先级后重排一次，并递增 `epoch`。
- 提供语义化常量：`PriorityFirst=0`、`PriorityEarly=10`、`PriorityNormal=50`（默认）、`PriorityLate=90`、`PriorityLast=100`。

**边界**
- 排序必须在锁内完成；`epoch` 用于让匹配侧判断快照是否过期。
- 空路由列表不得 panic。

**验收**
- 注册 A(P=50)、B(P=10)、C(P=50)，执行顺序为 B → A → C。
- 反复增删后相同集合的顺序稳定。

---

### F-10 Rule / Handler 分离 · P0

**价值**：把"是否匹配"与"匹配后做什么"拆开，使匹配逻辑可复用、可组合、可单独测试。

**规格**
- `type Rule func(*Ctx) bool`；`type Handler func(*Ctx)`。
- **Rule 只做判断与解析**，把解析结果写入 `Ctx.State`；**Handler 只做业务**，从 `State` 取参数。
- 约定键名常量集中定义（`StateKeyCommand`、`StateKeyArgs`、`StateKeyRegexMatch`、`StateKeyKeepPrefix`、`StateKeyImageURLs`…），禁止散落字符串字面量。
- Rule 可自由组合：`And`/`Or`/`Not` 组合子，以及 `R.All(rules...)`。

**边界**
- Rule 内不得起 goroutine（会被判定为快速返回而使超时失效）。
- Rule panic 由调度层 recover（见 F-12），不得影响其他路由。
- 同一 Rule 可能被多个事件并发调用，必须无副作用或只写自己的 `Ctx.State`（每事件独立）。

**验收**
- 组合子真值表测试（And/Or/Not 各 4 组输入）。
- 一个 Rule 只判断、一个 Handler 只消费 State 的端到端用例。

---

### F-11 事件上下文与 State · P0

**价值**：一次事件处理过程中的共享数据（解析出的参数、临时标记）需要一个容器，且必须**每事件独立**。

**规格**
- `type Ctx struct { ctx context.Context; bot *Bot; Event *Event; State State; caller Caller; mu sync.Mutex; once sync.Once; cached string }`
- `type State map[string]any`，提供类型安全访问器：`GetString(k)`、`GetInt64(k)`、`GetBool(k)`（缺失返回零值 + false）。
- 生命周期：随事件结束而废弃；不跨事件保留。需要跨事件的用 Session（F-21）。
- 保留键机制：以常量 `StateKeyKeepPrefix`（值 `__keep__`）开头的键在某条路由未匹配、准备尝试下一条时**不被清理**（用于跨路由传参）。
- `Ctx` 实现 `context.Context` 接口（`Deadline`/`Done`/`Err`/`Value` 转发到内部 ctx），使它可以像 ctx 一样传下去。
- `MessageString()` 用 `sync.Once` 缓存纯文本，供多条规则复用。

**边界**
- `Ctx` 不得被 Handler 保存到长生命周期结构里（事件结束即失效）。
- 并发写 `State` 必须通过 `SetState`（加锁）或保证单 goroutine。

**验收**
- 两条路由串联：第一条写 `__keep__x`，第二条仍能读到；普通键则被清理。
- `Ctx.Deadline()` 与内部 ctx 一致。

---

### F-12 热路径快照匹配 · P0

**价值**：每条事件都要遍历全部路由。若每次遍历都加锁，高并发下锁会成为瓶颈。

**规格**
- 路由表维护 `epoch uint64` 与只读快照 `snapshot []*Route`。
- 匹配前仅做一次加锁检查：若 `epoch != snapshotEpoch` 则重建快照并更新；否则直接使用旧快照（**零锁遍历**）。
- 快照是 `[]*Route`（切片头拷贝），路由对象本身在快照存续期内不可变。
- 快照重建时复制切片，不做深拷贝（Route 内部 Rules/Handlers 视为只读）。

**边界**
- 必须在高并发下验证：一个 goroutine 疯狂注册/删除，多个 goroutine 疯狂匹配，`-race` 无告警。
- `Route` 的可变字段（Priority 等）修改必须先加锁并递增 epoch。

**验收**
- 基准测试：`BenchmarkRouteMatch` 在 1000 条路由下 P99 < 100µs（不含 Handler 执行）。
- 并发注册 + 匹配，`-race` 通过。

---

### F-13 三段中间件钩子 · P0

**价值**：分群开关、限速、反并发、统计等横切关注点不应写进每个插件。用钩子把它们挂到引擎层。

**规格**
- `type Engine struct { pre []Rule; mid []Rule; post []Handler }`
- 执行顺序（对每条可能匹配的路由）：
  1. `pre`（全部通过才继续）——用途：黑白名单、功能开关、群组过滤
  2. 路由自身 `Rules`（全部通过才继续）
  3. `mid` ——用途：限速、单飞、并发闸门
  4. 路由 `Handlers`
  5. `post` ——用途：统计、指标、清理
- 钩子**按注册顺序**执行；`pre`/`mid` 返回 false 表示"本条路由放弃"，继续尝试下一条路由。
- 钩子可由插件包通过 `engine.Use(...)` 注册；也可按路由绑定（`Route.UsePre(...)`）实现精细控制。

**边界**
- post 钩子即使在 Handler panic 后也必须执行（用 defer 保证）。
- pre 钩子的拒绝必须可观测（计数 + 可选日志），否则"消息没反应"难以排查。

**验收**
- 注册 pre（拒绝某群）、mid（同用户第二次拒绝）、post（计数），断言三者顺序与效果。
- Handler panic 时 post 仍执行。

**易错点**
- 不要把 pre 钩子写在 match 内部（会导致超时后 goroutine 漂移）；钩子执行必须受同一个 ctx 控制。

---

### F-14 内置规则库 · P0

**价值**：80% 的插件都在写"命令匹配""关键词""@我"，内置可减少重复。

**规格**
- `Kind(kind string) Rule` ——匹配事件大类/细分（`"message/group"` 三级用 `/` 分隔）。
- `Command(cmds ...string) Rule` ——按配置的命令前缀匹配，裁掉前缀，参数写入 `StateKeyArgs`。
- `Prefix(ps ...string)`、`Suffix(ss ...string)`、`Keyword(ks ...string)`、`FullMatch(ss ...string)`、`Regex(pattern)`（子匹配写入 `StateKeyRegexMatch`）。
- `AtMe`、`OnlyGroup`、`OnlyPrivate`、`OnlyToMe`。
- 权限类：`SuperUser`、`GroupAdmin`、`GroupOwner`、`HigherThan(target func(*Ctx) int64)`。
- 媒体类：`HasImage`、`HasReply`（并把 URL/ID 写入 State）。
- `CheckUser(ids...)`、`CheckGroup(ids...)`。

**边界**
- Regex 必须**预编译**（在 `Regex()` 调用时），不得每条事件重新编译。
- 所有规则对空消息、nil Sender 必须安全返回 false（不得 panic）。
- `HigherThan` 需要查询群成员信息：必须使用带超时的 ctx，且失败时返回 false（fail-closed）。

**验收**
- 每条规则至少 3 组正例 + 3 组反例（表驱动）。
- nil Sender 的事件跑全部规则不 panic。

**易错点**
- 权限规则不得直接解引用 `ctx.Event.Sender.Role`：遇无 sender 的 notice 事件会 panic。

---

### F-15 一次性 / 临时路由 · P0

**价值**：某些规则只需匹配一次（例如"等待用户点确认"），匹配后应自动注销，避免长期占用。

**规格**
- `Route.Once(true)`：匹配并执行完成后自动从路由表移除。
- 支持 `expire time.Duration`：注册后若在期限内未匹配，自动注销（防止"注册后永不触发"的路由泄漏）。
- 注销通过 `Router.Remove(route)` 实现，内部递增 epoch。

**边界**
- 匹配与注销之间必须有原子性（同一次匹配流程内标记，避免被并发匹配两次）。
- 已注销的 Route 再次 Remove 必须幂等。
- 过期清理由后台 ticker 驱动（默认 10s 一次），不得为每条临时路由起一个 goroutine。

**验收**
- Once 路由在第二次匹配时不再执行。
- 注册带 100ms 过期的路由，200ms 后路由表长度恢复。

**易错点**
- 临时路由若"注册后永不触发"却不回收，长期运行会缓慢泄漏。

---

### F-16 交互式等待（Await/Stream）· P0

**价值**：多轮交互（"请输入歌名" → 等用户下一条消息）如果用状态机写会非常啰嗦。把它变成"等一个 channel"。

**规格**
- `func (c *Ctx) Await(ctx context.Context, kind string, rules ...Rule) (*Ctx, error)` ——阻塞直到下一个满足条件的**同会话**事件到达，或 ctx 超时。临时路由登记在当前 `Ctx` 所属 Session 名下（见 F-21）。
- `func (c *Ctx) Stream(ctx context.Context, kind string, rules ...Rule) (<-chan *Ctx, func())` ——持续接收若干事件，返回取消函数。
- `func (c *Ctx) Take(ctx context.Context, n int, kind string, rules ...Rule) ([]*Ctx, error)` ——收满 n 条。
- **会话连续性**由内置 `SameSession` 规则保证：`UserID` 与 `GroupID` 都相同。
- 实现：注册一条 `Once` + 高优先级临时路由，其 Handler 把 `Ctx` 发到定向 channel。
- 传入的 ctx 与 `Ctx` 自身 ctx 取**较短**的那个作为超时。

**边界**
- **取消必须幂等**：返回的 cancel 被调用两次不得 panic（用 `sync.Once`）。
- **超时后必须注销临时路由**，且不得留下阻塞的 goroutine；临时路由必须登记到当前 `Ctx` 所属 Session 名下，使 F-21 回收会话时能一并注销。
- 若调用方不接收 channel 且 ctx 未取消 → 必须有超时兜底，不得永久阻塞（channel 容量 1 + 非阻塞发送）。
- Await 的临时路由优先级必须**小于**当前路由（否则会被自己的规则再次截获）。

**验收**
- Await 在 3 秒超时内收到第 2 条消息，返回其 Ctx。
- cancel 调用两次，第二次无 panic。
- 超时后路由表长度恢复（无泄漏）。
- `-race` 下 100 并发 Await 全部正确配对（不串台）。

**易错点**
- 反面做法：cancel 直接 `close(done)`，二次调用 panic；channel 无人接收则 goroutine 永久阻塞。

---

### F-17 单飞（反并发）中间件 · P1

**价值**：同一用户连点两次，不应触发两次昂贵操作。

**规格**
- `type Singleflight[K comparable] struct { mu sync.Mutex; inflight map[K]struct{} }`
- 作为 `mid` 钩子挂载：key 已存在 → 拒绝（可选回一句"正在处理"）；不存在 → 占位并放行。
- 释放时机：`post` 钩子（含 panic 场景，用 defer）。
- key 由用户提供：`WithKey(func(*Ctx) K)`，常用 `UserID`、`UserID+GroupID`。
- 提供 `OnReject(func(*Ctx))` 回调。

**边界**
- **必须**保证释放：即使 Handler panic 也要释放（不能依赖 finalizer——时机不确定）。
- 占位表的清理必须用 defer，且 key 不存在时删除必须安全（不得裸类型断言）。
- 进程重启后占位表自然清空（不持久化）。

**验收**
- 并发两次同 key 调用，第二次被拒绝且回调被触发。
- Handler panic 后再调用同 key 能通过（占位已释放）。

---

### F-18 令牌桶限速中间件 · P1

**价值**：防止单用户/单群刷屏打爆 LLM 额度。

**规格**
- `type Limiter struct { mu sync.Mutex; tokens float64; last time.Time; rate float64; burst float64 }`，`AllowN(n) bool`（先按经过时间补币再扣）。
- `type LimiterManager[K comparable]` 按 key 惰性创建 Limiter，并用 TTL 缓存回收空闲条目（TTL = `burst/rate * 3`）。
- 挂载为 `mid` 钩子：`engine.UseMid(limiter.Rule(func(c *Ctx) int64 { return c.Event.UserID }))`。
- 可配置多档：每分钟 N 次（默认）、每群每分钟 M 次。

**边界**
- `AllowN` 必须并发安全（单锁即可，热点在 key 分散时无争用）。
- 超过容量的 key 必须被 TTL 回收，否则 map 无界增长。
- 系统时间回拨时不得出现负数 token（`last` 大于 now 时按 0 补币）。

**验收**
- burst=3、rate=1/s：连续 3 次通过，第 4 次拒绝；等待 1s 后再通过 1 次。
- 10 万个不同 key 后，TTL 到期 map 大小回落。

---

### F-19 功能开关中间件 · P1

**价值**：群管需要一个"本群关闭该插件"的开关，且重启后要保留。

**规格**
- `type Toggle struct { store Store; defaultOn bool }`；key = `(pluginName, groupID)`。
- 挂载为 `pre` 钩子：命中且为关 → 拒绝（静默或提示"本功能已关闭"）。
- Store 抽象：`Get(key) (bool, bool)`、`Set(key, bool)`、`Delete(key)`，内置内存与文件两种实现。
- 提供管理命令：`/switch <plugin> on|off`（限管理员）。
- 批量查询：`EnabledGroups(plugin) []int64`，便于管理面板。

**边界**
- Store 读失败时**按 defaultOn 处理**并告警（可用性优先），但必须在日志中显式记录。
- 插件名必须在编译期/启动期校验存在，防止开了一个不存在的开关。

**验收**
- 关闭后该群不触发；其他群不受影响；重启后状态保持。

---

### F-20 背压队列 · P2

**价值**：事件洪峰（群被刷屏、批量撤回）时不应无限起 goroutine。

**规格**
- 环形缓冲或 `chan Event` + 固定 worker 数（默认 `max(4, GOMAXPROCS)`）。
- 可选策略：`DropOldest`（覆盖最旧）、`DropNewest`（拒绝新事件并计数）、`Block`（阻塞上报侧）。
- 默认 `DropNewest` + 指标计数，队列容量 1024。
- 事件入队时记录 enqueue 时间，出队时记录队列等待时长（指标）。

**边界**
- 队列满时必须**丢弃并计数**，不得阻塞 Driver 的读循环（否则会把平台连接拖垮）。
- worker panic 必须 recover 并保持 worker 存活。
- 关闭时把队列内容处理完或明确丢弃（可配置 drain 超时）。

**验收**
- 注入 10 倍容量的事件，进程不 OOM、worker 数不增长、丢弃计数正确。
- 关闭时 1s 内退出。

---


### F-81 命令参数解析 · P1

**价值**：`/ban <id> [duration]`、`/switch <plugin> on|off` 这类命令需要把参数串变成结构化参数。裸 `strings.Fields` 处理不了引号与转义，也不提供类型校验与用法提示。

**规格**
- `func ParseCommandArgs(s string) ([]string, error)`：手写 shellwords 词法——支持单/双引号、反斜杠转义、连续空白折叠；未闭合引号返回错误。
- ~~`func BindFlags(v any, args []string) error`~~：**已删除**。该反射绑定没有任何生产调用方（只有自己的测试），
  属于纯表面积，移除后命令参数只保留 `ParseCommandArgs` 的词法解析；将来真需要结构化 flag 时再按需引入。
- `Command` 规则（F-14）解析后把**参数切片**写入 `StateKeyArgs`：类型是 `[]string`，不是拼接后的字符串。
- 解析或绑定失败 → 回一句用法提示（走 F-55 出口）并终止本条路由，不进入 Handler。
- 管理命令（F-71）统一基于本 Feature，不各自解析。

**边界**
- 不支持的类型必须返回 error，**不得** `panic`。
- 未定义的 flag 默认报错；可用 tag 显式开启忽略。
- 空参数串返回空切片而非 nil（便于测试断言）。

**验收**
- 表驱动：带引号、带转义、未闭合引号、空串、重复空白。
- 断言 `StateKeyArgs` 的静态类型为 `[]string`。

---

## 3. 会话与状态

### F-21 Session 与 Manager · P0

**价值**：会话级状态（历史、人格、限速计数器、临时路由）需要归属。把它挂在"每个群/每个用户一个 Session 实例"上，可以用**实例隔离代替库内加锁**。

**规格**
- `type Session struct { ID SessionKey; Hist History; Persona string; caller Caller; mu sync.Mutex; lastSeen time.Time; data map[string]any }`
- `type SessionKey struct { SelfID, GroupID, UserID int64 }`；`func (k SessionKey) String() string` 返回 `"selfID:groupID:userID"`，日志与指标里的 `session_key` **一律**用它，禁止各自拼接；策略可配：
  - `PerGroup`（默认）：一个群一个 Session，`UserID=0`
  - `PerUser`：一个用户一个 Session
  - `PerUserInGroup`：群内按用户细分
- `type Manager struct { mu sync.Mutex; sessions map[SessionKey]*Session; policy Policy; ttl time.Duration; max int }`
- `GetOrCreate(key) *Session`：不存在则创建；超过 `max` 时按 LRU 淘汰。
- 后台 ticker（默认 1 分钟）清理 `lastSeen` 超过 `ttl`（默认 30 分钟）的空闲会话。
- Session 内的所有可变状态必须经 Session 的方法访问（内部加锁），不允许外部直接改字段。

**边界**
- `max` 必须有限制（默认 10,000），防止被大量陌生群/用户撑爆内存。
- 淘汰时必须先落盘/固化需要持久的状态（如书签记忆 F-48）。
- 会话被清理后，其上挂载的临时路由（F-15/F-16）也必须被注销。

**验收**
- 并发 1000 个不同 key 调用 `GetOrCreate`，`-race` 通过，实例数不超过 max。
- 超过 TTL 后会话被回收，内存不持续增长。

---

### F-22 状态注入（反射绑定）· P1

**价值**：Rule 把解析结果写进 `State map[string]any`，Handler 里反复 `.(string)` 断言既啰嗦又易 panic。用 struct tag 自动回填。

**规格**
- `func (c *Ctx) Bind(v any) error`：遍历 `v` 的字段，按 `bot:"stateKey"` tag 从 `State` 取值并赋给字段。
- 字段映射结果（`[]struct{Index int; Key string}`）按 `reflect.Type` 缓存进 `sync.Map`，避免每次遍历全部字段。
- 支持类型：string / int / int64 / bool / float64 / []string / any；不支持的类型跳过并返回错误。
- 提供预置模型：`CommandModel{Command, Args}`、`RegexModel{Match []string}`、`ImageModel{URLs []string}`。

**边界**
- key 不存在时保持零值，**不报错**（让可选参数自然为零值）。
- 类型不匹配时返回明确错误（含字段名与期望类型），不得 panic。
- 必须对 nil 入参、非指针入参返回错误。

**验收**
- 表驱动：字符串/整型/布尔/切片字段正确回填；缺失 key 得到零值；类型错误返回 error。
- 基准测试：`BenchmarkBind` 在缓存命中路径下 < 1µs。

---

### F-23 会话生命周期与回收 · P1

**价值**：长期运行的机器人必须能释放不再活跃的资源，否则内存与路由表会缓慢泄漏。

**规格**
- 统一的生命周期接口：`type Closer interface { Close(ctx context.Context) error }`。
- Manager 定期回收会触发：Session 关闭 → 注销其临时路由 → 固化记忆 → 从 LRU 移除。
- 进程退出时 `Bot.Shutdown(ctx)` 按逆序关闭；**关闭顺序与超时的唯一定义在 F-70**，F-23 只负责把"关闭全部 Session"接入该序列。
- 所有后台 goroutine（ticker、worker、watcher）都注册到 `Bot` 的 WaitGroup，Shutdown 时等待退出。

**边界**
- Shutdown 的总超时上限以 F-70 为准（默认 10s），超时后强制返回并记录未退出的组件。
- Shutdown 幂等：重复调用不再执行。
- 回收不得在持有锁时执行用户代码（避免死锁）：先收集待回收列表，释放锁后再逐个关闭。

**验收**
- 创建 100 个会话后触发回收，goroutine 数与内存回落。
- Shutdown 后无残留 goroutine（`goleak` 或 runtime.NumGoroutine 断言）。

---

### F-24 配置热加载 · P1

**价值**：改提示词、改开关、调限速不应重启进程。

**规格**
- 观察的目标：提示词/人格目录、功能开关文件、限速参数、敏感词表、**权限表**（F-53/F-54）。
- 用 `fsnotify` 监听目录；事件做 **debounce**（默认 300ms），避免编辑器写入过程触发多次。
- 加载后**原子替换**：`atomic.Pointer[Config]` 或 `atomic.Value`，读侧无锁。
- 加载失败时**保留旧配置**并告警，绝不清空。
- 每次替换记录版本号与来源文件哈希，供 `/config reload` 管理命令展示。

**边界**
- 写了一半的文件可能解析失败 → 必须重试（最多 3 次，间隔 100ms）后才判定失败。
- 替换必须是整体替换，不允许"部分字段更新、部分保留"（避免不一致状态）。
- 监听 goroutine 必须随 Shutdown 退出。

**验收**
- 修改文件后 1s 内新配置生效（旧请求不受影响）。
- 写入非法内容时旧配置仍可用，且产生一条告警。

---

### F-25 配置校验与 fail-fast · P0

**价值**：配置写错应在**启动时**报错退出，而不是在第一条用户消息时崩溃。

**规格**
- 每个配置项声明元信息：类型、是否必填、默认值、取值范围、敏感标记。
- 启动时执行 `Validate() error`，收集**全部**错误后一次性返回（不是遇到第一个就停），便于一次改完。
- 必填项缺失、枚举非法、数值越界、文件不存在、URL 非法 → 全部报错退出。
- 敏感项（token/key）在日志中脱敏（只显示前 4 位 + 长度）。
- 提供 `--check-config` 子命令：只校验并打印最终生效配置（脱敏），不启动服务。

**边界**
- 需要区分"未设置"与"设置为零值"：**一律用 `*T` 指针**表达（不引入 `Option[T]`），禁止用"0 即未设置"。
- 校验失败必须退出码非 0，便于容器编排判定。

**验收**
- 缺失必填项时输出全部缺失项并退出码 1。
- 显式设置数值为零值时被接受（不被误判为未设置）。

**易错点**
- 用"0 表示未设置"会导致 YAML 显式写 0 无法覆盖全局非零值。

---

### F-82 作用域配置与人格路由键 · P1

**价值**：一份"全局默认 + 按人格/场景覆盖"的配置模型，让同一个人格的多项配置集中在一处；人格名本身就是"当前处于哪套设定"的完整状态，可持久化、可迁移。

**规格**
- `func (c *Config) Get(scope, key string) (any, bool)`：查找顺序 `scope 专属 → 全局默认`，缺失时递归回退；`scope` 为空则直接取全局。
- 人格作用域文件放 `prompts/personas/<name>.yml`：`go:embed` 内置默认版本，运行时可被同名外部文件覆盖。
- `Session.Persona`（F-21）就是作用域键，且**随会话状态持久化**；切换人格 = 改这个键，不重建 Session。
- 提供 `func Resolve[T any](c *Config, scope, key string) (T, error)` 泛型取值；类型不符返回明确错误。
- 取值结果参与 F-65 的"半静态段"：人格变更应使该段哈希变化（属预期，记一条日志）。

**边界**
- 覆盖文件解析失败 → 保留全局默认并告警，不中断服务（与 F-24 一致）。
- 未知 scope 不得静默返回零值：`Resolve` 必须返回明确错误。
- 作用域名（人格名）必须在启动期校验；引用了不存在的人格 → 启动失败（F-25 fail-fast）。

**验收**
- 全局设 A、人格 `x` 设 B → 人格 `x` 取到 B，其他人格取到 A。
- 删除人格覆盖文件后回退到 A，服务不中断。
- 切换人格后 F-65 半静态段哈希发生变化。

---


### F-83 嵌入式持久层与版本化迁移 · P0

**价值**：JSONL 文件在"单进程顺序追加"下够用，但一旦需要按条件查询、跨文件事务、字段演进，就会退化成"把文件全读进内存再筛"。一个嵌入式数据库把这些变成 SQL；而选**嵌入式**而非服务端，是为了保住"单二进制 + 同目录配置"这个部署形态（决策 16）。

**规格**
- 选型：`modernc.org/sqlite`（纯 Go，无 cgo），单文件数据库，路径由配置给出
- 打开即启用 **WAL**；`busy_timeout` 取**短值**（默认 1s），不用默认的长值
- 写竞争处理：应用层重试（抖动 20–150ms，最多 15 次）；写事务一律 `BEGIN IMMEDIATE`，让锁竞争在**事务开始时**暴露，而不是执行到一半才发现
- 每 N 次成功写入做一次 PASSIVE checkpoint（默认 50），避免 WAL 无限增长
- **单写者纪律**：进程内所有写经同一句柄串行。跨进程并发写不在支持范围（与决策 16 的形态一致），此约束必须写进文档而不是默认大家知道
- 迁移：`schema_version` 单行表 + 版本门控迁移链。**纯加列**走声明式对账（列缺失则 `ADD COLUMN`，幂等）；改索引、改数据、改全文索引才进版本链
- 迁移必须**可重入**：中断后重跑与一次跑完结果一致
- 既有 `history.jsonl` / `memory.jsonl` 提供一次性导入，**幂等**（按内容指纹去重），导入后原文件保留不动

**边界**
- 数据库打不开时**启动失败**，不得静默降级为内存——那会悄悄丢数据，而"悄悄"是这类问题的全部危害
- 迁移失败必须回滚，并保留可读的失败版本号
- 时间一律存 **Unix 毫秒整数**，避免本地时区歧义
- 不做自动清理与 vacuum 之外的维护动作；库大小作为指标暴露

**验收**
- 空库迁到当前版本，`schema_version` 正确
- 迁移中断后重跑，结果与一次跑完一致
- 16 个 goroutine 并发读写，`-race` 干净，且无 `database is locked` 逃逸
- JSONL 导入两次的结果与导入一次相同

---

### F-84 消息归档与全文检索 · P0

**价值**：对话历史现在只能整段读出，"召回"靠子串扫描。落库后可以按时间、角色、关键词、平台来源检索，也可以只取**片段**——`recall_history` 才真正具备"从很久以前捞回一条"的能力。

**规格**
- `messages` 表：`(id 自增, session_key, seq, role, content, tool_calls JSON, tool_name, tool_call_id, created_at, token_count)`
- 索引 `(session_key, seq)` 与 `(session_key, created_at)`
- **全文检索**：FTS5 虚表覆盖 `content`，由 INSERT/UPDATE/DELETE 触发器保持同步
- **中文必须可用**：默认分词对中文按整段切词，检索会退化成整句匹配。必须启用 CJK 友好的分词（trigram 或等价方案），并针对"子串检索"单独给出验收
- 检索返回**片段**（命中处上下文）与前后各一条消息，而不是整段历史
- 查询输入必须**清洗**：未配对引号、悬空布尔运算符、连字符词都先规范化再交给 FTS，不把语法错误抛给上层
- `recall_history` 改走检索；**只查当前 `session_key`**，取不到会话时仍然失败而非查全库（沿用既有约束）

**边界**
- 单条正文上限与截断标记沿用 F-38 的既有约定
- 工具轮次必须与 assistant 消息**成对**存储；按单条删除时要维护配对，或明确禁止单独删除
- `seq` 在会话内单调递增且不重复，不允许跳号覆盖
- 检索结果按会话内位置稳定排序，同样查询两次结果一致

**验收**
- 中文关键词能命中（含子串场景），英文同理
- 检索只返回本会话内容，跨会话隔离有测试
- 含引号/布尔符的畸形查询不报错，返回可用结果
- 删除一条消息后 FTS 不再命中它

---

### F-85 会话台账与用量归集 · P1

**价值**：缓存命中率与 token 消耗目前只写进日志——能看，不能查、不能回归。落库后可以回答"上周命中率多少""哪类会话最贵""换前缀后收益多少"，F-74 的指标也不再依赖外部系统。

**规格**
- `sessions` 表：`(session_key 主键, 首次出现, 最后活动, 消息数, 工具调用数, input/output/cache_hit/cache_miss/reasoning tokens, 请求数, 估计成本, 价格版本)`
- 每次 LLM 调用后**原子累加**，不整行覆盖（并发下会丢增量）
- 缓存命中率与成本按**会话**与**全局**两级查询
- 价格表带**版本号**：供应商改价后，老数据仍能按当时的版本解释
- 会话归档时固化最终值，之后不再变化

**边界**
- 用量写入失败**不得**影响回复主流程（与 F-49 的"固化失败不影响主流程"同一原则）
- 计数必须单调不减；发现回退即告警（说明发生了并发覆盖）
- 不存提示词全文（那是 F-89 的范围），避免表膨胀

**验收**
- 单会话多轮调用后，token 与请求数等于各轮之和
- 并发调用同一会话，累加无丢失
- 缓存率查询结果与同轮日志打印值一致

---

## 4. LLM 接入层

### F-26 统一 LLM 接口 · P0

**价值**：上层业务不应知道背后是 OpenAI、Claude 还是本地 Ollama。接口一旦定错（例如把具体客户端类型写进结构体字段），抽象就形同虚设。

**规格**
- `type LLM interface { Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error); ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) }`
- `ChatRequest` 字段：`Messages []Message`、`Tools []ToolSpec`、`Temperature float64`、`MaxTokens int`、`ResponseFormat *ResponseFormat`、`Metadata map[string]string`。
- `ChatResponse` 字段：`Content string`、`ToolCalls []ToolCall`、`FinishReason string`、`Usage Usage`。
- **所有上层结构体字段必须是 `LLM` 接口类型**，禁止出现 `*OpenAIClient` 之类的具体类型。
- 每个实现加编译期断言：`var _ LLM = (*OpenAI)(nil)`。

**边界**
- `ChatStream` 必须在 ctx 取消时关闭 channel（且只关闭一次）。
- 空 Messages 必须返回明确错误。
- 实现之间可互相包装（`RetryLLM`、`CacheLLM`、`RouterLLM` 都实现 `LLM`）。

**验收**
- 用 fake LLM 替换后，Agent 全链路测试无需网络。
- `var _ LLM` 断言在编译期捕获接口漂移。

---

### F-27 Provider 注册与多供应商路由 · P1

**价值**：单一供应商会抖动/限流/涨价。多供应商 + 路由策略让降级与成本优化不改业务代码。

**规格**
- 注册：`Register(name string, factory func(Config) (LLM, error))`；配置里用 `provider: openai` 选择。
- `type Router struct { primary LLM; fallbacks []LLM; strategy Strategy }`，实现 `LLM`。
- 策略枚举：
  - `Primary`：只用主
  - `Fallback`：主失败（可重试错误）→ 依次尝试备用
  - `Cost`：按配置的每千 token 价格选最便宜
  - `Speed`：按滑动窗口内的 P95 延迟选最快
  - `RoundRobin`：轮询
- 健康检查：连续 N 次失败后把该 provider 标记为不健康 `cooldown`（默认 60s），期间跳过。

**边界**
- 只有"可重试错误"（5xx、429、超时）才触发 fallback；4xx 参数错误直接返回（否则会重复计费）。
- fallback 链必须有最大长度（默认 3），避免雪崩式重试。
- 每次 fallback 必须记录指标（provider、原因、耗时）。

**验收**
- 主 provider 返回 500 时自动切到备用并成功返回。
- 主 provider 返回 400 时不 fallback，直接返回错误。
- 不健康 provider 在 cooldown 内被跳过。

---

### F-28 流式契约 · P0

**价值**：流式输出让用户感知更快；但流式最容易泄漏 goroutine。

**规格**
- `type Chunk struct { Content string; ToolCalls []ToolCall; FinishReason string; Done bool; Err error }`
- 生产者负责 `close(ch)`；消费者 `for c := range ch`。
- **错误也走 channel**（`Err` 字段 + `Done=true`），不额外返回 error channel。
- **所有发送必须监听 ctx**：`select { case ch <- c: case <-ctx.Done(): return }`。
- channel 建议带缓冲（默认 16），降低生产者阻塞概率。
- 约定：收到 `Done=true` 的 chunk 后消费者应停止读取（channel 随后关闭）。

**边界**
- 上游 EOF 与上游错误都必须转成一个终止 chunk 后关闭。
- 消费者提前 `break` 后，生产者必须能通过 ctx 取消感知并退出（消费侧应 `defer cancel()`）。
- 禁止向已关闭的 channel 发送。

**验收**
- 消费者读 3 个 chunk 后 break + cancel，生产者 goroutine 在 100ms 内退出（`goleak` 验证）。
- 上游返回错误时，最后一个 chunk 的 `Err` 非空且 `Done` 为 true。

---

### F-29 流式工具调用聚合 · P1

**价值**：OpenAI 流式返回的 tool_calls 是**按 index 分片**的：第一个分片只有 name，后续分片是 arguments 的增量。不聚合就无法执行工具。

**规格**
- 维护 `map[int]*ToolCall`：`index → {ID, Name, ArgumentsBuilder}`。
- 每个分片：若 index 不存在则创建；`ID`/`Name` 只在首次出现时设置；`Arguments` 按字符串追加。
- 收到 `finish_reason == "tool_calls"` 或流结束时，把 map 中所有条目转为完整的 `[]ToolCall` 输出。
- 保证输出顺序按 index 升序（map 遍历无序，必须排序）。

**边界**
- 分片可能跨多个 SSE 事件，且 JSON 参数可能被截断在任意位置 → 必须**纯字符串拼接**，不逐片 `json.Unmarshal`。
- 同一 index 出现两次 name 时以首次为准（后续为异常，记录告警）。
- 流中途出错时，已聚合的完整调用可以选择丢弃或保留（默认丢弃，避免执行半截参数）。

**验收**
- 用分段构造的 SSE 事件序列（arguments 被切成 5 片），聚合结果与完整 JSON 逐字节相等。
- 多工具并行调用（index 0/1/2 交错）时输出顺序为 0,1,2。

---

### F-30 重试与退避 · P0

**价值**：上游抖动是常态，业务代码不该为它写循环。

**规格**
- `type RetryPolicy struct { MaxAttempts int; BaseDelay time.Duration; MaxDelay time.Duration; Factor float64; Jitter bool; Retryable func(error) bool }`
- 默认：`MaxAttempts=3`、`BaseDelay=500ms`、`MaxDelay=30s`、`Factor=2`、`Jitter=true`。
- 退避等待必须**可被 ctx 中断**：`select { case <-time.After(d): case <-ctx.Done(): return ctx.Err() }`。
- 默认 `Retryable`：网络错误、超时、429、5xx 为可重试；400/401/403/404 不可重试。
- 尊重 `Retry-After` 响应头（若有）。
- 实现为 `LLM` 装饰器，对 `Chat` 与 `ChatStream` 分别处理（流式一旦开始输出就不再重试，避免重复内容）。

**边界**
- 总耗时必须有上限（`MaxAttempts × MaxDelay`），且受 ctx 约束。
- 每次重试记录指标（尝试序号、错误类型、等待时长）。

**验收**
- 前两次返回 500、第三次成功 → 最终返回成功且尝试 3 次。
- 返回 400 → 只尝试 1 次。
- ctx 在等待期间取消 → 立即返回 `ctx.Err()`。

---

### F-31 结构化输出 · P1

**价值**：解析模型自由文本易碎；让模型按 schema 输出可显著提升可解析率。

**规格**
- `type ResponseFormat struct { Type string; Schema json.RawMessage; Strict bool }`；`Type` ∈ {`text`, `json_object`, `json_schema`}。
- 提供 `func JSONSchemaOf[T any]() ResponseFormat`：用反射生成基础 JSON Schema（object/array/string/number/boolean、required、enum）。
- Provider 不支持 `json_schema` 时自动降级为 `json_object` 并在提示词里附加 schema 文本。
- 解析失败时最多重试 1 次，并在重试提示中附上失败原因。

**边界**
- Schema 必须是合法的 JSON Schema 子集；生成后自校验一次，非法则启动期报错。
- 严格模式下字段缺失应报错而不是静默零值。

**验收**
- `JSONSchemaOf[MyStruct]()` 产出可被标准校验器接受的 schema。
- 模型返回非法 JSON 时触发一次重试。

---

### F-32 Token 计量与上下文预算 · P1

**价值**：上下文超限会被服务端拒绝；用字节数估算会严重偏差。

**规格**
- `type Counter interface { Count(ctx context.Context, model string, msgs []Message) (int, error) }`
- 实现优先级：provider 返回的真实 usage > 本地 tokenizer（若可用）> 启发式估算（中文 ~1.5 字符/token、英文 ~4 字符/token）。
- `type Budget struct { MaxContext int; ReserveOutput int; ReserveTools int }`；可用输入 = `MaxContext - ReserveOutput - ReserveTools`。
- 组装请求前调用 `Fit(msgs) []Message`：超预算时按策略裁剪。
- 累计统计：每次调用记录 prompt/completion/total，按会话与全局聚合（供 F-66）。

**边界**
- 估算与真实值偏差需记录（用于校准启发式系数）。
- 裁剪后必须保证 system 提示词（若标记为 pinned）**永不被裁掉**。
- 工具 schema 本身占用的 token 必须计入预算。

**验收**
- 构造超长历史，`Fit` 后估算 token 数 ≤ 预算。
- pinned 的 system 消息在任意裁剪下都存在。

---

### F-33 提示词模板引擎 · P0

**价值**：提示词是最常改、最易改坏的资产，需要"可版本化 + 可测试 + 不重启生效"。

**规格**
- 模板文件放 `prompts/**/*.tmpl`，用 `//go:embed prompts` 内置**默认**模板；运行时可用同名文件覆盖。
- 引擎用 `text/template`，**命名占位**（`{{.Nickname}}`）而非位置参数（`%v`）。
- 内置函数：`now`、`timezone`、`join`、`quote`、`mdTable`（渲染 Markdown 表格）。
- 渲染前**统一行尾**：把模板与渲染结果中的 `\r\n` 归一为 `\n`（**这条是硬性要求**）。
- 模板语法错误在**启动期**检出（用样例数据预渲染一次），不是首次使用时才炸。
- 渲染结果计算哈希并缓存，哈希不变则复用（省 CPU）。

**边界**
- 模板中引用了不存在的变量 → 启动期报错（用 `Option("missingkey=error")`）。
- 单次渲染耗时上限（默认 50ms），超时告警。
- 模板文件缺失时回退到内置版本，不中断服务。

**验收**
- 在 CRLF 与 LF 两种检出的模板下渲染结果**逐字节相同**。
- 模板写错变量名时启动失败并指出行号。

**易错点**
- 用 `//go:embed` 加 `fmt.Sprintf` 位置参数渲染提示词，顺序写错不报错，且易被 CRLF 击穿黄金测试。

---

## 5. Agent 能力层

### F-34 统一 Agent 契约 · P0

**价值**：ReAct、Reflexion、Orchestrator 等不同范式应共用同一个出口，HTTP 层与 IM 层不必为每种范式写分支。

**规格**
- `type Agent interface { Run(ctx context.Context, in Input) (*Output, error) }`
- `type Input struct { Query string; History []Message; SessionKey SessionKey; Files []Attachment }`
- `type Output struct { Text string; Steps []Step; Usage Usage; ToolCalls []ToolCall; FinishReason string }`
- `type Step struct { Type string; Content string; ToolName string; ToolInput string; ToolOutput string; DurationMS int64; Error string }`；`Type` ∈ {`thought`, `action`, `observation`}。
- **不设 `Metadata map[string]any`**：需要传递的字段必须显式出现在结构体上（否则会出现"写了读不到"的死字段）。
- 所有实现加 `var _ Agent = (*ReAct)(nil)` 断言。

**边界**
- `Run` 必须在 ctx 取消时尽快返回。
- `Steps` 用于可观测与调试，必须在任何退出路径上都填充完整。
- `Output.Text` 为空但 `ToolCalls` 非空时属于合法结果（`ToolCalls` 是**本轮已执行的调用记录**，不是待执行队列）。

**验收**
- 两种 Agent 实现互换，上层 Handler 代码零修改。

---

### F-35 ReAct 循环 · P0

**价值**：让模型能"先想、再调工具、看结果、再想"，是 Agent 的核心循环。

**规格**
- 循环：`for i := 0; i < MaxIterations; i++` { 调用 LLM（带工具 schema） → 若有 tool_calls 则逐个执行并追加 observation → 否则返回最终答案 }。
- 默认 `MaxIterations = 10`，可配；达到上限返回明确错误（含已完成的 Steps）。
- **工具消息协议必须正确**：assistant 消息带 `tool_calls` 数组（含 `id`、`name`、`arguments`），tool 结果消息带 `tool_call_id` 指向对应调用。**不能**把 tool call 塞进 `content` 字符串。
- 一轮内多个工具调用：默认**串行**执行（保证顺序可预测）；提供 `ParallelTools(true)` 选项，但要求工具声明 `ConcurrencySafe`。
- 每步记录 `Step`（thought/action/observation）与耗时。

**边界**
- 工具执行失败必须作为 observation 回灌（`{"error": "..."}`），**不中断循环**，让模型自行纠错。
- 单步工具执行超时（默认 30s）必须可配，且超时后仍要回灌超时信息；**需要人工审批的工具（F-45），审批等待占用独立预算，不计入该单步超时**。
- ctx 取消时立即返回。
- 循环内累计 usage，最终汇总返回。

**验收**
- **关键契约测试**：构造"两轮工具调用"的会话，断言第二轮请求中 assistant 消息含 `tool_calls`、tool 消息含匹配的 `tool_call_id`（用 mock server 校验请求体）。
- 达到 MaxIterations 时返回错误且 Steps 完整。
- 工具返回 error 时循环继续而非终止。

**易错点**
- 把 tool call 序列化成 JSON 塞进 `content`、消息转换只映射 role/content，会导致第二轮被服务端 400 拒绝。

---

### F-36 Reflexion 自我反思 · P2

**价值**：对失败答案让模型自我批评并重试，可提升复杂任务成功率。

**规格**
- 流程：`Run` → 评估（自评或规则评估）→ 未达标则生成 reflection 文本 → 带 reflection 重试，最多 `MaxReflections`（默认 1）次。
- `type ReflexionConfig { MaxReflections int; Evaluator Evaluator; ReflectPrompt string }`。
- `type Evaluator interface { Evaluate(ctx context.Context, in Input, out *Output) (score float64, reason string, err error) }`。
- reflection 文本追加到下一次的 system 或 user 消息中（不修改历史）。

**边界**
- **反思状态必须属于单次 Run，不得放在 Agent 结构体上**（否则并发请求会串数据）：把 episodic memory 放在 Agent 字段上就是无锁的跨请求竞争。
- 每次反思都要记录 Step 与 token 消耗。
- 评估失败（Evaluator 返回 error）时应直接返回首次结果，而不是报错。

**验收**
- 并发 50 个 Run，各自的 reflection 内容不串（`-race` 通过）。
- Evaluator 达标时只调用 1 次 LLM。

---

### F-37 Orchestrator-Workers · P2

**价值**：复杂任务可分解为多个子任务并行执行。

**规格**
- 流程：规划（LLM 产出 `Plan{Subtasks, Deps}`）→ 拓扑分层 → 同层并行（信号量限流 `MaxWorkers`）→ 汇总（LLM 综合）。
- `type Plan struct { Analysis string; Subtasks []Subtask; Dependencies map[string][]string }`；`Subtask{ID, Description, WorkerType, Input}`。
- `type Worker struct { Name string; SystemPrompt string; Tools []string; agent Agent }`。
- 并行度用 `semaphore chan struct{}` 控制（默认 4）；结果收集用 `WaitGroup` + 互斥写入 map。
- 单个子任务失败不终止整体：其 `Result.Success=false`，依赖它的子任务跳过并标记 skipped。

**边界**
- 依赖图必须检测环（规划产物不可信）→ 有环则回退为串行执行并告警。
- 每个子任务有独立超时。
- 汇总输入必须截断到预算内（子任务输出可能极长）。

**验收**
- 给定菱形依赖图（A→B,C→D），B 与 C 并行执行（用 sleep 计数验证并发）。
- 环依赖时不死锁，回退串行。

---

### F-38 对话历史管理 · P0

**价值**：历史是 LLM 应用的通用难点（裁剪、配对、持久化），应抽成可替换组件而非散落各处。

**规格**
- `type History interface { Append(ctx, key, item) error; Messages(ctx, key) ([]Message, error); Reset(ctx, key) error; Trim(ctx, key, n int) error }`
- 条目类型：`UserMessage` / `AssistantMessage` / `ToolCall` / `ToolResult` / `Marker`（标记，如"重置点"）。
- 内置实现：`MemoryHistory`（环形缓冲，默认 50 条）、`FileHistory`（JSONL 追加，便于排查）。
- 裁剪策略可插拔，**默认 `Window(n)`，n=50**：
  - `Window(n)`：保留最近 n 条（保证 tool 调用与其结果的配对完整）
  - `TokenBudget(counter, n)`：按 token 预算从新到旧保留
  - `Summarize`：超限时用 LLM 摘要旧段（摘要本身也计入预算）
- **配对完整性**：裁剪时不得把 assistant 的 `tool_calls` 与其后的 `tool` 结果拆散。

**边界**
- `Messages` 返回的切片必须是副本，调用方修改不得影响内部状态。
- 并发 Append/Messages 必须安全。
- 历史为空时返回空切片而非 nil（便于测试断言）。

**验收**
- 裁剪后断言不存在孤立的 tool 结果消息。
- 并发 Append 1000 条 + Messages，`-race` 通过，条目数正确。

---

### F-39 动作流解析 · P0

**价值**：模型可能一次输出多个动作（发消息 + 保存记忆），也可能把 JSON 包在代码块里。解析必须容错。

**规格**
- 输入：模型输出的原始文本。输出：`[]Action`（有序）。
- 解析步骤：
  1. 去除首尾空白；
  2. 若以代码块围栏包裹 → 剥离围栏；
  3. 逐个解码 JSON 对象（支持"多个对象连写"）：以显式扫描 `{` 边界为主，`dec.More()` 只作快路径，二者结果必须一致——**不依赖 `dec.More()` 的顶层非文档语义**；
  4. **必须启用 `UseNumber()`**（消息 ID/QQ 号是 int64，float64 会静默丢精度）；
  5. 跳过 `action` 为空的条目；
  6. 遇到非法片段 → 记录并从该位置尝试恢复（跳到下一个 `{`），完全无法恢复才返回错误。
- 返回结果附带 `ParseWarning` 列表（哪些片段被跳过）。

**边界**
- 输入为纯文本（无 JSON）时返回空动作列表 + 明确错误，不得 panic。
- 单个动作的 `params` 非对象时跳过该动作并告警。
- 动作数量上限（默认 16），超出截断并告警（防模型刷屏）。

**验收**
- 表驱动：干净 JSON、代码块包裹、多对象连写、尾随逗号、截断、纯文本、嵌套对象、超大整数，共 8 组。
- 大整数（如 `1234567890123456789`）解析后精度不丢。

**易错点**
- 不用 `UseNumber()` 会把 int64 变成 float64 而静默丢精度。

---

### F-40 虚拟动作闭环 · P1

**价值**：有些"动作"不需要宿主执行（结束本轮、保存记忆），应由库自己处理，否则模型会一直等待一个永远不会来的响应。

**规格**
- 保留动作名：`end_action`（结束本轮，不回复）、`save_memory`（写长期记忆）、`noop`（显式空操作）。
- 执行流程：解析出动作 → 若为虚拟动作则由库内部执行 → **伪造一条 `"ok"` 的结果回灌历史**，保持"调用→观察"循环完整。
- 非虚拟动作返回给宿主执行，宿主把真实结果回灌。
- 用哨兵错误表达控制流：`var ErrEndOfTurn = errors.New("end of turn")`，**不使用 io.EOF**（语义模糊，易与网络错误混淆）。

**边界**
- `save_memory` 的参数必须是单行文本（含换行则拒绝并回灌错误结果）。
- 空记忆拒绝写入。
- 虚拟动作也必须纳入审计日志（F-60）。

**验收**
- 模型输出 `end_action` 后循环终止且不发送任何消息。
- `save_memory` 写入成功后在下次会话的提示词中出现该记忆。

---


## 6. 工具系统

### F-41 工具注册表 · P0

**价值**：Agent 能用什么能力由注册表决定。它必须是并发安全的、可自省的，并能一键导出成模型的 function schema。

**规格**
- `type Registry struct { mu sync.RWMutex; tools map[string]Tool; order []string }`。
- `Register(t Tool) error`（重名返回错误）、`MustRegister`（重名 panic，仅用于 init 阶段）、`Get(name) (Tool, bool)`、`List() []Tool`（**按注册顺序**，保证提示词稳定）、`Names() []string`。
- `Definitions() []ToolSpec`：导出给 LLM 的 function schema。
- `Subset(names ...string) *Registry`：派生子注册表，供不同 Worker 使用不同的工具集（F-37）。
- `Remove(name)`、`Clear()`（主要供测试）。

**边界**
- `List` 顺序必须稳定（用 `order` 切片维护），否则每次生成的提示词不同 → 破坏 prompt cache（F-65）。
- 并发 Register 与 Definitions 必须安全。
- `MustRegister` 仅在 init 期使用，运行期禁止。

**验收**
- 注册 3 个工具，两次 `Definitions()` 结果逐字节相同（顺序稳定）。
- `Subset` 不影响父注册表。

---

### F-42 自描述工具接口 · P0

**价值**：工具要能被模型正确调用，必须自己描述"我是谁、参数是什么"。

**规格**
- `type Tool interface { Name() string; Description() string; Parameters() Schema; Execute(ctx context.Context, args json.RawMessage) (Result, error) }`
- `type Schema struct { Type string; Properties map[string]Property; Required []string }`；`Property{Type, Description, Enum, Default, Minimum, Maximum}`。
- `type Result struct { Output string; Error string; Metadata map[string]string }`；提供 `Success(out)`、`Failure(msg)`、`(Result) String()`。
- `Execute` 的入参用 `json.RawMessage`（避免二次序列化，且允许工具自定义解码）。
- 工具可声明属性：`ReadOnly() bool`、`ConcurrencySafe() bool`、`Dangerous() bool`（用于 F-45 审批）。

**边界**
- `Name()` 必须匹配 `^[a-zA-Z0-9_-]{1,64}$`（OpenAI 要求），非法名称在 `Register` 时拒绝。
- `Description()` 长度上限（默认 1024 字符），超出截断并告警。
- `Execute` 返回的 error 会被包装成 observation 回灌模型，**不得 panic**。

**验收**
- 非法的工具名注册时返回 error。
- `Result.String()` 在 `Error` 非空时返回错误文本。

---

### F-43 泛型参数解析 · P1

**价值**：每个工具都写一遍 `json.Unmarshal` + 字段校验很啰嗦。

**规格**
- `func ParseArgs[T any](raw json.RawMessage) (T, error)`：解码 + 必填校验。
- 支持 tag：`arg:"name"`（重命名）、`arg:"name,required"`、`arg:"name,enum=a|b|c"`、`arg:',default=10'`。
- 校验失败返回结构化错误：`type ArgError struct { Field, Rule, Got string }`，便于回灌给模型自我纠正。
- 参数缺失且无默认值且为必填 → 错误信息中明确列出**所有**缺失字段（不是只报第一个）。

**边界**
- 未知字段：默认忽略（兼容模型多传），可通过 tag 开启严格模式报错。
- 类型不匹配（模型传字符串给整型字段）→ 尝试宽松转换后仍失败才报错。

**验收**
- 表驱动：必填缺失、枚举非法、默认值生效、类型宽松转换、多字段同时出错。

---

### F-44 内置安全工具集 · P1

**价值**：开箱可用的基础能力，且必须**零注入面**。

**规格**
- `calculator`：用 `go/parser` 解析算式，白名单节点（`BasicLit`/`BinaryExpr`/`UnaryExpr`/`ParenExpr`），递归求值。**禁止 `eval`、禁止 `exec`**。支持 `+ - * / %` 与括号（`%` 仅对整数取模）；数字上限（默认 1e15），除零返回错误。
- `current_time`：返回指定时区的 RFC3339 时间，参数校验时区合法性。
- `http_fetch`：GET 一个 URL 并返回文本，**必须**：仅允许 http/https、拒绝私网地址（SSRF 防护，见 F-59）、超时 10s、响应体上限 1 MiB、非文本类型拒绝、可选域名白名单。
- `memory_save` / `memory_recall`：包装 F-48。
- `json_query`：对给定 JSON 用路径表达式取值（只读，无代码执行）。

**边界**
- 每个工具都必须声明超时（默认 10s 或更短）。
- `http_fetch` 的 DNS 解析结果也必须校验（防 DNS rebinding：解析后检查 IP 是否为私网）。
- 工具输出长度上限（默认 8 KiB），超出截断并标注 `[truncated]`。

**验收**
- 计算器：`__import__("os")`、`1+`、`((1+2)*3` 全部返回错误而不 panic。
- http_fetch 访问 `http://127.0.0.1/` 与 `http://169.254.169.254/` 被拒绝。
- 超大响应被截断且标注。

---

### F-45 工具权限与人工审批 · P1

**价值**：有些工具（踢人、禁言、发钱）不能由模型自由调用，需要角色校验或人工确认。

**规格**
- `type Authz struct { AllowedRoles []Role; RequireApproval bool; RateLimit *RateLimit }`；按工具名配置。
- 权限判定在**执行前**进行：不满足 → 返回错误结果并回灌模型（让它知道"你没权限"），而不是静默失败。
- 审批流程：`RequireApproval` 的工具调用会生成一条待审批记录，向指定管理员发送确认请求（利用 F-16 Await 等待"是/否"），超时（默认 60s）视为拒绝。
- 审批结果必须记入审计日志（F-60）与 Step。

**边界**
- 审批等待必须占用独立的超时预算，不能吃掉 LLM 的 ctx 超时（否则用户还没点，ctx 就超时了）。
- 同一审批请求必须幂等：重复点击只生效一次。
- 审批被拒绝时回灌明确文本 `approval denied by admin`。

**验收**
- 越权调用返回权限错误且工具未执行（用 spy 断言 `Execute` 未被调用）。
- 审批超时后按拒绝处理并回灌。

---

### F-46 工具执行沙箱 · P2

**价值**：若未来引入"执行代码/命令"类工具，必须有资源与权限边界。

**规格**
- 独立进程执行（`os/exec`），受限：CPU 时间上限、内存上限、输出上限、执行时长上限。
- 工作目录限定在临时沙箱目录，只读挂载必要资源。
- 网络默认**禁止**；需要时按工具显式开启。
- 平台相关的隔离能力（Windows Job Object / Linux cgroup）通过构建标签分别实现；不支持的平台默认禁用该类工具。

**边界**
- 必须能在超时后**强杀整个进程组**（含子进程），不留孤儿。
- 输出必须流式读取并限长，避免子进程写满管道导致死锁。

**验收**
- 死循环脚本在超时后被杀死，父进程无残留子进程。
- 超长输出被截断而不阻塞。

---


### F-86 在途操作的持久化（可恢复的等待与审批）· P2

**价值**：F-16 的 `Await` 与 F-45 的审批目前都是**进程内**状态。重启即丢——一次审批可能已经问过人了，但确认回来时机器人已经忘了在等什么。持久化后等待可跨重启、可审计、可超时清理。

**规格**
- `pending` 表：`(id, session_key, kind, 负载 JSON, 创建时间, 到期时间, 状态)`
- 覆盖两类：等待下一条消息（F-16）、等待人工审批（F-45）
- 启动时恢复未过期记录；已过期的标记为超时并**回灌**给原会话，让对话能继续而不是悬空
- 到期清理必须**有界**：后台定时清理 + 条数上限
- 状态迁移可审计（谁在何时批准/拒绝/超时）

**边界**
- **审批等待不得占用单步超时**（F-45 已确立的约束在此继续成立）
- 恢复时若原会话已不存在，标记为孤儿并告警，**不猜测**投递目标
- 崩溃发生在"已写入待审批、尚未通知用户"之间时，允许重复通知；用户侧按 id 幂等
- 不做分布式锁：仍是单进程语义

**验收**
- 写入待审批 → 重启 → 仍能读到并继续
- 超时后原会话收到明确回灌，记录状态为超时
- 长跑下 `pending` 表不无界增长

---

## 7. 记忆与知识

### F-47 记忆抽象 · P0

**价值**：记忆实现会演进（内存 → 文件 → 数据库 → 向量库），接口必须稳定。

**规格**
- `type Memory interface { Save(ctx, scope Scope, item MemoryItem) error; Recall(ctx, scope Scope, query string, limit int) ([]MemoryItem, error); Forget(ctx, scope Scope, id string) error; List(ctx, scope Scope) ([]MemoryItem, error) }`
- `type Scope struct { SelfID, GroupID, UserID int64; Kind string }`（会话/用户/全局三种作用域）。
- `type MemoryItem struct { ID string; Text string; Title string; CreatedAt time.Time; Score float64; Refs []string }`。
- 首版实现：`MemoryStore`（进程内 map + 互斥锁），可选 JSONL 文件落盘；SQLite 实现在 **M3** 引入（与附录 A 一致），用 `modernc.org/sqlite`（纯 Go，不依赖 cgo）。

**边界**
- `Recall` 必须返回**副本**，调用方修改不影响内部。
- 单条记忆长度上限（默认 500 字符），超长截断并告警。
- 并发 Save/Recall 必须安全。
- 作用域必须严格隔离：群 A 的记忆不得出现在群 B 的回忆中。

**验收**
- 跨作用域召回隔离测试。
- 并发读写 `-race` 通过。

---

### F-48 书签式长期记忆 · P1

**价值**：长期记忆若存全文会迅速膨胀；若只存指针，开销极小且可回溯。

**规格**
- 每条记忆 = `{ID, Title, Text, Refs, CreatedAt, Score}`：
  - `Title`：用 LLM 生成的一句话标题（≤ 30 字），用于给用户展示与快速召回；
  - `Text`：要点正文（≤ 500 字）；
  - `Refs`：指向原始对话的引用（消息 ID 或历史片段 ID），需要时可回看原文。
- 写入时机：模型显式调用 `save_memory`（F-40），或规则触发（如"记住：xxx"命令）。
- 上限：每个作用域默认 200 条，超出按 LRU + 分值淘汰。
- 召回：按关键词/向量分数取 Top-K（默认 5），拼成提示词的一节。

**边界**
- 标题生成失败时回退为正文前 20 字，不得因 LLM 失败而丢失记忆。
- 相同内容重复保存时做去重（相似度 > 阈值则更新而非新增）。
- 淘汰必须可从索引与存储两侧一致删除。

**验收**
- 保存 → 新会话召回 → 出现在提示词中。
- 超过上限后最旧且分值最低的被淘汰。

---

### F-49 分层记忆 · P2

**价值**：短期上下文、情节、语义三类记忆的检索策略不同，分层能让"该记什么、该忘什么"显式化。

**规格**
- **Working**：当前会话最近 N 条（默认 50，与固化阈值一致），先进先出。
- **Episodic**：按"会话片段"聚合，超过空闲阈值（默认 30 分钟）开启新片段；支持按时间与关键词检索片段。
- **Semantic**：长期事实，经"固化"从 Working/Episodic 提炼而来。
- 召回预算：`working 1/2 + episodic 1/4 + semantic 1/4`，配额可按 token 计。
- 固化触发：Working 达到上限（默认 50 条）时异步固化，失败不影响主流程。

**边界**
- 固化必须**异步且可失败**：失败只告警，不得影响用户请求。
- 三层召回合并后必须去重并按相关度重排。
- 各层容量上限必须有界。

**验收**
- 注入 100 条消息后，Working 被裁剪、Episodic 生成片段、Semantic 有固化条目。
- 固化失败时主流程仍正常返回。

---

### F-50 二值向量检索 · P1

**价值**：不想引入外部向量库时，用"二值化 + 汉明距离 + 分桶"在 SQLite/内存里也能做够用的相似检索。

**规格**
- 向量二值化：`b[i] = (v[i] >= vtb) ? 1 : 0`（`vtb` 可配，默认 0），打包成 `[]byte`（64 维 = 8 字节）。
- 分桶：`group = round(‖v‖ × k) mod n`（`k`、`n` 可配，默认 k=2.0、n=64），减少比较次数。
- 查询：只在与查询同桶（可扩到相邻桶）的候选里算汉明距离，返回距离 ≤ 阈值（默认 8/64 ≈ 87.5%）的结果。
- 存储：`(id, text, vector BLOB, norm REAL, group_id INT)`，SQLite 建索引 `(group_id)`。
- 结果必须**按距离升序**返回。

**边界**
- **排序必须同时排 id**：用 `[]struct{Text string; ID int64; Distance int}` 一起排，禁止"排 texts 再单独排 ids"（极易错位）。
- 空库查询返回空结果，不报错。
- 距离计算必须用 `math/bits.OnesCount8` 等位运算，逐字节比较。
- 每次插入后 norm 与 group 必须与向量一致。

**验收**
- 构造 1000 条已知向量，查询 Top-5 与暴力搜索结果一致（同桶+相邻桶覆盖时）。
- 排序正确性：结果的距离单调不减，且文本与 ID 一一对应（打乱插入顺序后仍成立）。

**易错点**
- 并行数组分开排序（只排 texts 不排 ids）会导致缓存答案挂到错误的问题上。

---

### F-51 混合检索 · P2

**价值**：纯向量检索对"专有名词、ID、命令"效果差；纯关键词对同义表达差。

**规格**
- 两路召回：BM25/关键词（倒排或简单 TF-IDF）+ 向量（F-50）。
- 融合用 RRF（Reciprocal Rank Fusion）：`score = Σ 1/(k + rank_i)`，k 默认 60。
- 可选重排：用小模型或规则对 Top-20 重排取 Top-5。
- 两路权重、Top-K 均可配。

**边界**
- 任一路失败时降级为单路（记录降级原因），不整体失败。
- 融合结果必须去重（同一文档被两路命中）。

**验收**
- 专有名词查询（关键词路命中）与同义改写查询（向量路命中）都能返回正确结果。
- 单路失败时仍返回结果。

---

### F-52 摘要树（RAPTOR 式）· P2

**价值**：长文档问答中，"先检索摘要层再下钻原文"能覆盖更宏观的问题。

**规格**
- 递归构建：叶子 = 文档块 → 按相似度聚类 → 每簇生成摘要 → 摘要作为上一层节点 → 重复至 `MaxLevels`（默认 3）或节点数 < `MinCluster`。
- 检索：在每一层都做检索，合并结果（宏观问题命中上层摘要，细节问题命中叶子）。
- 节点结构：`{ID, Level, Content, Summary, Vector, Children, Parent, Refs}`。

**边界**
- **摘要失败必须降级**：退化为"拼接 + 截断"，不得中断整树构建。
- **embedding 失败必须跳过该簇**并继续其他簇。
- 构建必须有总体超时与进度记录（可断点续建）。
- 树规模上限，防止聚类退化为单链。

**验收**
- 注入 100 个文档块，构建出 ≥ 2 层结构。
- 注入摘要失败的 mock 后构建仍完成，且失败簇有记录。

---


### F-87 记忆持久化与写入决策 · P0

**价值**：记忆目前是 JSONL + 进程内扫描，写入只做"近似去重后追加"。落库后可以按作用域与分值检索、记录来源，并在写入时做出真正的**新增 / 更新 / 忽略**决策，而不是只能追加。

**规格**
- `memories` 表：`(id, scope_key, kind, title, text, source_refs JSON, created_at, updated_at, score, 内容指纹)`
- `scope_key` 必须参与**所有**读写路径；跨作用域串读视为缺陷（F-47）
- 写入决策：把新事实与同作用域既有记忆比对，得出**新增 / 更新（并入既有条目）/ 忽略（重复）**三种结论
- 去重判据必须**可解释**：给出相似度与判定理由，便于调阈值与排查误合并
- 排序必须**确定性**（同分时按 id）；否则同一份记忆两次渲染不同，会平白失效前缀缓存
- 写入与注入的位置继续遵循 ADR-0002

**边界**
- 单条长度上限沿用 F-47（500 字符）
- 内容指纹用于幂等：同样的写入重放不产生新条目
- **就地更新**，不得改变条目顺序——否则每次更新都让记忆段整体位移
- 相似度阈值可配；**误合并的代价高于漏合并**，默认取保守值

**验收**
- 同一事实不同措辞写入两次，只有一条
- 两个不同事实都保留
- 更新既有条目后，注入顺序不变
- 跨作用域隔离有测试

---

### F-88 记忆的遗忘、导出与留存 · P1

**价值**：记忆现在只能写不能删。用户要求"忘掉这个"、或要导出自己的数据时没有通道；留存策略也缺失——长期运行下记忆只会单调增长。

**规格**
- `Forget(scope, id)` 与 `ForgetAll(scope)`：删除必须**同时**从索引与存储两侧一致移除
- `List(scope)`：按时间倒序返回，供用户检视
- **导出**：按作用域导出为可读格式（JSONL），字段与内部表一致
- 留存：每作用域条数上限（默认 200，对齐 F-48），超出按 **LRU + 分值** 淘汰
- 淘汰必须有日志，且可从索引与存储两侧一致删除
- "忘记我"这类请求走**显式工具或命令**，不依赖模型记忆

**边界**
- 删除**幂等**：删不存在的 id 不算错误
- 导出不得包含其它作用域的数据
- 淘汰优先级必须明确：新鲜的高分条目不得被同批次的低分新条目挤掉
- 删除后不影响其它条目顺序

**验收**
- 删除后检索与注入都不再出现该条
- 导出内容与 `List` 一致，且不含跨作用域数据
- 超过上限后，最旧且分值最低的被淘汰

---

## 8. 安全与合规

### F-53 权限即提示词 · P0

**价值**：只做"执行前拦截"时，模型仍会不断尝试越权调用，浪费轮次并可能触发危险动作。把"你只能调这些"直接写进提示词，可大幅降低越权尝试。

**规格**
- 权限表用 YAML 声明（`actions.yaml`）：每个 action 的 `desc`/`params`/`data`，以及每个角色的允许列表。
- 渲染：`Policy.Render(role) string` 把该角色的允许 action 渲染成 **Markdown 表格**（列：功能/action/params/data），注入系统提示词的固定位置。
- 提示词中必须显式声明："**列表中没有的 action 不允许调用**"。
- 双层防护：提示词做输入侧约束，执行前 `Policy.Allow(role, action)` 做硬拦截（F-54）。
- 权限表用 `//go:embed` 内置默认版本，允许运维用外部文件覆盖。

**边界**
- YAML 中引用了未定义的 action → **启动期报错**（不是运行时 panic）。
- 渲染结果必须稳定（同样的表每次渲染逐字节相同），以便 F-74 黄金测试与 F-65 提示词缓存。
- 角色不存在时返回明确错误，不得静默返回空表（空表会让模型以为无权限而被困惑）。

**验收**
- 渲染结果与 `testdata/policy-<role>.golden` 逐字节相等。
- 引用了未定义 action 的 YAML 在启动时被拒绝。
- 提示词中确实包含"列表中没有的 action 不允许调用"字样。

---

### F-54 权限判定缓存 · P0

**价值**：权限检查在每次动作解析时都会执行，必须 O(1)。

**规格**
- 声明用切片（`Roles[role] = []string`，便于 YAML 编写与阅读），判定用 set（`map[string]struct{}`）。
- 首次访问某角色时把切片折叠成 set 并缓存；后续查表 O(1)。
- 缓存用 `sync.RWMutex` + `map[Role]map[string]struct{}`（不引入第三方并发 Map）。
- 权限表热加载（F-24）时**必须整体失效缓存**。

**边界**
- 缓存填充必须是"未命中时填充"（注意布尔方向）。
- 未知角色必须返回 `false`（fail-closed），不是 panic 也不是 true。
- 热加载后旧缓存不得残留。

**验收**
- 首次 `Allow` 填充缓存，第二次不再重新折叠（用计数 mock 验证）。
- 热加载后新权限立即生效。
- 未知角色返回 false。

**易错点**
- 缓存填充的条件写反（`if ok` 而非 `if !ok`）会导致缓存永不建立。

---

### F-55 统一出口过滤链 · P0

**价值**：安全策略（脱敏、敏感词替换、格式规整）若散落在各发送点，新增一条发送路径就会漏掉。

**规格**
- **所有**对外发送必须经过 `Sender.Send(ctx, target, msg)` 这一个出口；禁止任何旁路调用底层 Caller 直接发送。
- 出口依次执行的处理链（顺序固定）：
  1. 长度/段数限制（超长截断并标注）
  2. 敏感词替换（F-56）
  3. 去噪（连续空行、多余空白、可选去 emoji）
  4. 文本替换（`ReplaceTextOut`，可配替换表）
  5. 尾部剪裁（去掉结尾换行/空格）
  6. 格式规范化（可选繁简转换）
  7. 审计记录（F-60）
- 处理链可插拔（`type OutboundFilter func(string) string`），顺序可配但默认顺序固定。
- 每个 filter 必须可单独关闭（便于定位问题）。

**边界**
- 任一 filter panic 必须被 recover，且**放行原始内容**（可用性优先）并告警。
- 过滤后为空时不得发送（视为"无内容"），但要记录审计。
- 过滤必须是**幂等**的（对已过滤内容再过滤结果不变），否则重试会二次改写。

**验收**
- 断言所有发送路径都经过过滤链（用 RecordingCaller 校验内容已被处理）。
- 注入 panic 的 filter 后仍能发送。
- 对同一内容连续过滤两次结果相同。

---

### F-56 敏感词引擎（AC 自动机）· P1

**价值**：敏感词表可能上万条，逐词 `strings.Replace` 是 O(词数 × 文本长度)。

**规格**
- Aho-Corasick 自动机：`type acNode struct { children map[rune]*acNode; fail *acNode; outputs []Match; length int; replacement string }`。
- 构建：插入全部模式 → BFS 构建 fail 指针 → 得到可并发只读查询的自动机。
- 查询：单次扫描文本得到 `[]Replacement{Start, End, Replacement}`。
- **重叠处理**：按 `Start` 升序、同起点取最长、跳过与已选区间重叠的匹配，然后从后往前一次性重建字符串（避免下标偏移）。
- 白名单：另一个自动机，命中白名单区间的匹配被剔除。
- 支持大小写不敏感、全角/半角归一，以及 `\uXXXX` / `&#xXX;` 转义形态的归一（防止用转义绕过词表；归一只在查询侧做，不改变原文）。

**边界**
- 自动机一旦构建完成即**不可变**，可被多 goroutine 并发查询（无锁）。
- 重建字符串必须按**从后往前**替换，否则前面替换会破坏后面的下标。
- 空模式、超长模式（默认 > 64 字符）在构建期拒绝。
- 词表热加载时构建**新**自动机并原子替换指针，不阻塞查询。

**验收**
- 重叠用例：模式 `{abc, bc, bcd}` 对文本 `abcd` 得到确定且互不重叠的替换结果。
- 1 万词表对 1KB 文本查询 < 1ms。
- 并发查询 `-race` 通过。

---

### F-57 入站内容审查 · P1

**价值**：用户可能发送诱导性内容（提示词注入、越狱）。入站审查可提前拦截。

**规格**
- `type InboundGuard interface { Check(ctx context.Context, msg Message, meta Meta) (*Verdict, error) }`；`Verdict{Allow bool; Reason string; Score float64}`。
- 内置实现：
  - `RuleGuard`：正则/关键词规则（如"忽略以上所有指令"）
  - `LLMGuard`：调用小模型做分类（可选，异步/带超时）
  - `VectorGuard`：与已知恶意语料做相似度比对（复用 F-50）
- 挂在 `pre` 钩子：拒绝时按配置静默、或回复预设话术、或仅记录。

**边界**
- 审查必须有**超时**且超时按"放行 + 告警"处理（fail-open）——除非配置为 fail-closed（高风险场景）。
- 拒绝必须记入审计（含命中规则与评分），便于申诉与调参。
- LLMGuard 的调用必须计入成本统计（F-66）。

**验收**
- 命中规则时被拒绝且审计有记录。
- 审查超时（mock 慢 guard）时按配置放行/拒绝。

---

### F-58 黑名单与防刷 · P1

**价值**：恶意用户/群会刷爆额度或扰乱服务。

**规格**
- 黑名单维度：用户 ID、群 ID、IP（若适用）；支持**带过期时间**的临时封禁。
- 存储：**复用 F-19 的 `Store` 接口**（同一实现，不另起一套），支持带过期时间的条目。
- 判定挂 `pre` 钩子，命中直接拦截（按配置静默或回固定话术）。
- 自动防刷：N 秒内超过 M 条消息 → 临时封禁 T 秒（默认 10 秒 20 条 → 封 60 秒），并记录指标。
- 管理命令：`/ban <id> [duration]`、`/unban <id>`、`/banlist`。

**边界**
- 黑名单查询必须 O(1) 且并发安全（读多写少，用 RWMutex 或原子快照）。
- 管理命令必须校验调用者权限（F-14 的 `SuperUser`）。
- 封禁不得把机器人自己或超管封掉（白名单优先）。

**验收**
- 拉黑后立即拦截；过期后自动恢复。
- 触发防刷后被临时封禁，时间到自动解封。

**运维扩展（后加，与上面同属"谁能用它"）**
- **名单（`access` 分节）**：`allow`（白名单）/ `deny`（黑名单），按 **QQ 号**与**群号**配置，
  命中即在**路由层直接丢弃**——不匹配路由、不建会话、不落库、不调用模型。
  与黑名单的分工：名单只看得到 QQ/群号（最彻底），需要看内容的审查仍走 moderation。
- **角色（`access.roles`）**：用 QQ 号直接指定 `superuser` / `owner` / `admin` / `member`，
  优先于平台上报的群成员角色；同一 QQ 出现在多个角色时按权限取高。
- **超管名单是并集**：`moderation.super_users` ∪ `access.roles.superuser`（管理命令授权、
  权限判定、名单绕过都读这一份）。默认超管绕过名单，避免"配错白名单把自己锁在门外"。
- 每次丢弃进审计（`inbound_blocked`）与 `events_dropped{reason="access"}` 指标。

---

### F-59 出站 HTTP 安全 · P0

**价值**：任何"根据用户输入去请求 URL"的功能都是 SSRF 与内存放大攻击的入口。

**规格**
- 统一构造 HTTP 客户端：`Timeout`（默认 10s）、`DialContext` 超时、`TLSHandshakeTimeout`、连接池上限、`CheckRedirect` 限制跳转次数（默认 3）且重新校验每一跳的目标地址。
- 响应体用 `io.LimitReader`（默认 1 MiB），超限即断开并报错。
- **SSRF 防护**：解析目标 host → 得到 IP → 拒绝回环、私网（10/8、172.16/12、192.168/16、169.254/16）、链路本地、组播、保留地址，以及 IPv6 的 `::1`、`fc00::/7`、`fe80::/10`；**必须对最终连接的 IP 校验**（防 DNS rebinding，用 `DialContext` 拿到实际 IP 再判断）。
- 仅允许 `http`/`https` scheme；可选域名白名单。
- 解压炸弹防护：限制解压后大小。

**边界**
- 图片等二进制内容必须先看 `Content-Length` 与 `Content-Type`，再决定是否解码；缩放应在解码**之前**按像素上限拒绝（例如声明尺寸超 8192×8192 直接拒绝）。
- 任何本地文件路径必须 `filepath.Clean` 后做绝对路径前缀校验，**禁止**用 `HasPrefix` 判断目录包含关系（前缀可被路径穿越绕过）。
- 重定向到私网地址必须拒绝。
- 所有失败路径必须有超时兜底，不得无限等待。

**验收**
- 请求 `127.0.0.1`、`10.0.0.1`、`169.254.169.254`、重定向到私网 → 全部被拒绝。
- 3 MiB 响应被截断并报错（不 OOM）。
- 声明尺寸 100000×100000 的图片被拒绝且未分配大内存。

---

### F-60 审计日志 · P1

**价值**：出问题时需要回答"谁在什么时候让机器人做了什么"。

**规格**
- 审计事件类型：`llm_call`、`tool_call`、`action_exec`、`virtual_action`、`policy_denied`、`approval`、`inbound_blocked`、`rate_limited`、`config_changed`。
- 每条记录：时间戳、trace_id、session_key、触发者（user/group）、动作名、结果（ok/denied/error）、耗时、token 消耗、脱敏后的关键参数。
- 输出：结构化 JSON 行（便于采集）；通过注入的 `io.Writer` 支持落文件与 stdout 两种去处（默认文件，测试用 buffer）。
- 审计**不可关闭**（只可调级别）；高敏动作（踢人/禁言/封禁）必须记录。

**边界**
- 审计中的用户内容需按配置脱敏（例如只记录长度与前 20 字）。
- 审计写入失败不得影响主流程（异步 + 有界队列 + 丢弃计数）。
- 审计日志不得包含密钥、token。

**验收**
- 每次工具调用与策略拒绝都产生一条审计记录。
- 审计队列满时主流程不受影响，且有丢弃计数。

---

### F-61 密钥管理 · P1

**价值**：API key 泄露是最常见的严重事故。

**规格**
- 来源优先级：环境变量 > 密钥文件（Unix 下要求 0600；Windows 下用 ACL 校验等价权限，不满足时告警但不阻断）> 配置内联（不推荐，启动时告警）。
- 日志与错误信息中**一律脱敏**：只显示前 4 位与长度（`sk-1***（len=51）`）。
- 提供 `secrets.Redactor` 中间件，对任意字符串做模式替换（常见 key 前缀、`Bearer xxx`、`sk-`、`AKID` 等）。
- 启动打印"生效配置"时走 Redactor。
- 支持从文件热加载轮换（可选）。

**边界**
- 空密钥必须在启动期报错（若该 provider 启用）。
- 密钥文件权限不符时告警（不阻断）。
- panic 堆栈与 `http` 请求日志（若开启 debug）必须走同一 Redactor。

**验收**
- 全仓库搜索不到明文 key 的日志输出（用测试断言脱敏函数）。
- 误配空 key 时启动失败并指出配置项名。

---


## 9. 降本增效

### F-62 感知哈希图片去重 · P2

**价值**：视觉模型按图计费；群聊里同一张表情包/截图会被反复转发。

**规格**
- 对入站图片计算感知哈希（pHash，64 位）。
- 缓存 `pHash → 描述文本`，TTL 默认 24 小时，容量上限默认 10,000。
- 查询时与缓存中条目计算汉明距离，`distance ≤ 8`（≈87.5% 相似）即命中，**跳过视觉模型调用**。
- 命中后把描述**写回消息段**（`__derived_desc__` 字段），实现幂等：再次处理同一事件不再查询。
- 缓存必须分桶（按哈希前缀），禁止全量线性扫描。

**边界**
- 缓存条目的描述长度上限，避免单条过大。
- LRU + TTL 双重淘汰，防止无界增长。
- 哈希计算失败（非图片/损坏）时跳过该图并继续，不中断整体处理。

**验收**
- 同一图片两次入站：第二次不调用视觉模型（用计数器 spy 验证）。
- 缩放/轻微压缩后的同图仍命中（相似度阈值生效）。
- 10 万条目下查询耗时可接受（分桶生效，非 O(n)）。

**备注**
- 阈值与 TTL 必须可配置，不硬编码。

---

### F-63 语义缓存 · P2

**价值**：常见问题（"你是谁""怎么用"）会重复出现，命中缓存可直接回答，零 token 成本。

**规格**
- 缓存键 = 问题向量（F-50 二值化哈希）+ 会话上下文指纹（人格/系统提示词哈希）。
- 命中条件：向量相似度 ≥ 阈值（默认 0.95）**且**上下文指纹一致。
- 命中时直接返回缓存答案（可选择走一遍"改写"以适配当前上下文）。
- TTL 默认 1 小时；可按会话禁用；高波动话题（含时间/天气等）加入**跳过列表**。
- 缓存条目记录命中次数，低命中条目优先淘汰。

**边界**
- **不得**缓存包含实时信息的回答（跳过列表 + 可配置正则过滤）。
- 缓存必须按会话/人格隔离，避免答案串台。
- 答案写入缓存前必须经过出口过滤链（F-55）。

**验收**
- 相同问题第二次直接命中（无 LLM 调用）。
- 不同人格下同一问题不命中。
- 命中率与节省 token 数计入指标：`semcache_hits_total`、`semcache_misses_total`、`semcache_saved_tokens_total`。

---

### F-64 流式增量发送 · P1

**价值**：长回答逐字输出，用户感知延迟大幅降低；但逐字调用平台 API 会造成刷屏与限流。

**规格**
- 累积模式：收到流式分片先累积到缓冲区，按触发条件发送增量。
- 发送触发条件（满足其一）：
  - 遇到句末标点（`。！？.!?` 或换行）
  - 缓冲达到长度阈值（默认 40 字符）
  - 距上次发送超过时间阈值（默认 800ms）
  - 流结束（发送剩余）
- **前缀差发送**：发送内容 = "当前累积全文" 相对 "上次已发送前缀" 的**新增部分**；若平台支持"编辑消息"则发送全文更新，否则发送增量。
- 首次发送可以带"正在输入…"提示（若平台支持）。

**边界**
- 平台限流：发送频率上限（默认 1 次/800ms），超限时合并到下次发送。
- 增量计算必须处理"模型回退/重写"的情况（新内容不是旧内容的扩展）：此时选择重新发送或停止增量，不得发送错乱文本。
- 流结束时必须把**剩余缓冲**发出去，不能丢尾。
- 出错时已发送的内容不回滚（明示用户"回答中断"）。

**验收**
- 模拟 200 个分片、含标点，断言发送次数 ≤ 上限且最终拼接内容 == 完整回答。
- 缓冲剩余内容在流结束时被发出。
- 模型重写前缀时不产生重复/错乱文本。

---

### F-65 提示词前缀稳定化 · P2

**价值**：多数 provider 对"相同前缀"的提示词有缓存折扣（prompt caching）。前缀一变，折扣全失效。

**规格**
- 提示词按"变化频率"分三段，**固定顺序**：`[静态段][半静态段][动态段]`
- 静态段：系统指令、工具 schema、角色权限表（**在进程启动期内**逐字节稳定）
  - 半静态段：人格设定、长期记忆（按小时级变化）
  - 动态段：当前时间、最近对话、用户输入（每次都变）
- 静态段内容必须**逐字节稳定**：工具列表顺序固定（F-41 的 order）、权限表渲染顺序固定（F-53）、禁止在静态段里插入时间戳。
- 时间、随机数等动态内容**只能出现在动态段**。
- 提供 `/prompt-hash` 管理命令：打印各段哈希，便于确认缓存是否失效。

**边界**
- 会破坏稳定的因素必须显式排除：map 遍历顺序、goroutine 调度顺序、浮点格式化差异。
- 段哈希变化时记录一条日志（便于排查"为什么缓存没命中"）。
- 权限表/敏感词表热加载会**主动**改变静态段哈希并使 prompt cache 失效，属预期行为，必须记录一条日志。

**验收**
- 连续 100 次渲染（间隔注入不同时间），静态段哈希不变。
- 工具注册顺序打乱后静态段哈希**不**变（因为按 order 排序）。

---

### F-66 成本统计与配额 · P1

**价值**：LLM 成本是主要运营支出，必须可观测、可限额。

**规格**
- 计量：每次调用记录 `provider, model, prompt_tokens, completion_tokens, cost, latency, session_key, timestamp`。
- 价格表：`model → {input_per_1k, output_per_1k}` 可配置，未知模型按 0 计并告警。
- 聚合维度：全局、按会话、按用户、按日；提供 `/cost today`、`/cost session` 管理命令。
- 配额：`{scope, limit, period, action}`；超限动作可配，**默认 `deny`**：`deny`（拒绝请求并回复提示） / `downgrade`（切换到便宜模型） / `warn`（仅告警）。
- 预算预警：达到 80% / 100% 时各触发一次告警（去重）。

**边界**
- 统计写入必须异步且有界，不得阻塞请求路径。
- 进程重启后统计可有损（除非配置持久化），但**配额**若需严谨应持久化。
- 计费口径必须与 provider 返回的 usage 一致（优先用真实 usage，不用估算）。

**验收**
- 一次调用后全局与会话计数都 +1，成本按价格表计算正确。
- 超出配额时按配置动作生效。

---


### F-89 提示词快照与可重放 · P1

**价值**：前缀缓存的收益目前只能从日志事后推断；一旦怀疑"前缀为什么变了"，无法复现。保存**实际发出的报文**后，可以逐字节回放、量化前缀稳定性，并把它变成回归测试。

**规格**
- 每次 LLM 调用记录**实际发送的 `messages` 序列**（或指纹 + 可重建的快照），与返回的 usage 关联
- 提供**前缀稳定性**检查：同一会话相邻两轮，后一轮的前缀应以前一轮为前缀（增量追加）；否则记录**分歧点**
- 记忆更新、窗口滑动等**预期会改变前缀**的事件必须能被标注，从而与"意外前缀变化"区分开
- 快照用于**回归**：给定同一份输入，重建出的报文必须逐字节相同
- 存储有界：只保留最近 N 轮，或只保留"前缀变化点"的快照

**边界**
- 快照可能含用户隐私内容，留存与导出/删除策略需与 F-88 一致
- 记录必须是**旁路**：不能因为记录而改变实际发送的字节
- 快照的截断或压缩不得影响回放的字节一致性——宁可少存，不可存成"看起来一样"

**验收**
- 同一份历史重建两次，报文逐字节相同
- 窗口未滑动时，相邻两轮前缀满足前缀关系；滑动时能标出分歧点
- 开启快照后，实际请求字节与未开启时一致（有对比测试）

---

## 10. 可观测性与运维

### F-67 结构化日志与追踪 · P0

**价值**：出问题时需要能把一次事件的全过程串起来。

**规格**
- 使用 `log/slog`，输出 JSON（生产）或文本（开发）。
- 每条日志带：`trace_id`（每次事件生成）、`session_key`、`user_id`、`group_id`、`component`。
- 日志级别可配，且**组件级**可覆盖（如只把 llm 组件调到 debug）。
- 关键路径埋点：事件接收、路由匹配结果、LLM 请求/响应（内容按配置脱敏或省略）、工具调用、动作执行、发送。
- 所有日志走统一的 `Redactor`（F-61）。
- graceful 关闭时 flush 缓冲。

**边界**
- 高并发下日志不得成为瓶颈：异步写入 + 有界队列 + 丢弃计数。
- 消息内容默认**不记全文**（只记长度与摘要），避免隐私与体积问题；开启 `debug_content` 才记录。
- panic 恢复时必须记录堆栈（`debug.Stack()`）。

**验收**
- 一次事件产生的所有日志共享同一 trace_id。
- 关闭 debug_content 时日志中不出现用户消息全文。

---

### F-68 指标暴露 · P1

**价值**：需要在不看日志的情况下回答"现在健康吗、慢在哪"。

**规格**
- 暴露 `/metrics`（Prometheus 文本格式）。
- 核心指标：
  - `events_received_total{kind}`、`events_dropped_total{reason}`
  - `route_matches_total{route}`、`route_errors_total{route}`
  - `handler_duration_seconds{route}`（直方图）
  - `llm_requests_total{provider,model,status}`、`llm_tokens_total{provider,model,type}`、`llm_latency_seconds{provider,model}`
  - `tool_calls_total{tool,status}`、`tool_duration_seconds{tool}`
  - `actions_sent_total{action,status}`
  - `sessions_active`、`queue_depth`
  - `guard_blocks_total{guard,reason}`、`rate_limited_total{scope}`
- 指标必须在`同一处定义`（常量/结构体），清单与文档引用它，避免"配置抓取但没端点"。

**边界**
- 指标标签的基数必须受控（禁止把 user_id/group_id 作为标签，改放日志或聚合）。
- `/metrics` 端点使用**独立监听地址**（默认 `127.0.0.1:9090`），鉴权开关可选（默认关闭，因只监听回环）。

**验收**
- 抓取 `/metrics` 返回 200 且包含上述全部指标名。
- 标签基数在高并发下不爆炸（用压测断言时间序列数）。

---

### F-69 健康与就绪探针 · P1

**价值**：容器编排需要知道进程"活着"与"能服务"是两回事。

**规格**
- `GET /healthz`（liveness）：进程存活即 200，不检查下游（避免下游抖动导致被重启）。
- `GET /readyz`（readiness）：检查关键依赖（LLM provider 可达、存储可用、Driver 已连接）；任一不健康返回 503 并列出原因。
- 依赖检查结果带缓存（默认 10s），避免探针把下游压垮。
- 启动阶段区分 `starting`（返回 503）与 `ready`，避免冷启动被误判。

**边界**
- 探针必须快速返回（默认 1s 超时），不得阻塞。
- 探针路径与端口必须与部署清单使用**同一个常量来源**。

**验收**
- 停止依赖后 `/readyz` 返回 503，`/healthz` 仍 200。
- 探针 P99 < 50ms（缓存生效）。

---

### F-70 优雅关闭 · P0

**价值**：直接 kill 会丢失正在处理的请求、产生半截回复、留下损坏的文件。

**规格**
- `signal.NotifyContext(SIGINT, SIGTERM)` 触发 `Bot.Shutdown(ctx)`。
- 关闭顺序（**严格逆序**）：
  1. 停止接收新事件（Driver 停止上报）
  2. 等待在途事件处理完成（有超时，默认 10s）
  3. 停止后台任务（ticker、worker、watcher、固化任务）
  4. 关闭会话并固化需要持久的状态
  5. 关闭 Driver 连接
  6. flush 日志与指标，关闭存储
- 超时后强制返回，并列出未完成的组件。
- Shutdown 幂等。

**边界**
- 二次 SIGTERM 应**立即强制退出**（用户不耐烦时的逃生通道）。
- 必须等待所有 goroutine 退出（WaitGroup），测试中用 `goleak` 断言。
- 关闭期间不得再向已关闭的 channel/连接写入。

**验收**
- 处理中的请求在 Shutdown 后仍能完成（在超时内）。
- Shutdown 后 `goleak` 无发现。
- 连续两次 Shutdown 不 panic。

---

### F-71 管理命令 · P2

**价值**：运维需要在不重启的情况下观察与干预。

**规格**
- 通过消息命令（限超管）与本地 CLI 两条入口提供，**消息命令优先**，CLI 复用同一份命令实现：
  - `/status`：运行时长、事件数、活跃会话、队列深度、错误率
  - `/routes`：列出已注册路由（名称、类型、优先级、是否 Once）
  - `/cost today|session`：成本统计（F-66）
  - `/config reload`：手动触发热加载（F-24）
  - `/prompt-hash`：打印提示词各段哈希（F-65）
  - `/ban` / `/unban` / `/banlist`（F-58）
  - `/switch <plugin> on|off`（F-19）
- 所有管理命令必须鉴权、审计（F-60），且**不经过 LLM**（直接执行）。

**边界**
- 管理命令的输出也必须走出口过滤链（F-55）与长度限制（可能很长，需分页或截断）。
- 未授权调用必须静默忽略或明确拒绝（可配），但一定记审计。

**验收**
- 非超管调用 `/status` 被拒绝且审计有记录。
- `/config reload` 后配置确实更新。

---

### F-72 分布式追踪 · P2

**价值**：跨服务（机器人 → LLM 网关 → 工具后端）定位慢点。

**规格**
- 采用 OpenTelemetry：一次事件一个 trace，span 覆盖事件处理、路由匹配、每次 LLM 调用、每次工具执行、每次出站发送。
- trace context 通过 `context.Context` 传递（这也是 F-11 让 Ctx 实现 context.Context 的原因之一）。
- 采样率可配（默认 1%，可对错误强制采样）。

**边界**
- 未配置 OTLP endpoint 时必须是**零开销**（no-op provider），不得因为追踪不可用而影响功能。

**验收**
- 配置 endpoint 后能导出包含 LLM span 的 trace。
- 未配置时基准测试无性能回退。

---

## 11. 工程化与质量

### F-73 Lint 纪律 · P0

**价值**：有些错误一旦写进代码就很难根除（进程退出、panic、unsafe）。用 lint 从源头禁止。

**规格**
- `.golangci.yml` 采用 `disable-all: true` + **白名单**（显式列出启用的 linter），默认至少包含：`errcheck`、`govet`、`staticcheck`、`gosimple`、`ineffassign`、`unused`、`gocritic`、`bodyclose`、`contextcheck`、`errorlint`、`exhaustive`、`gosec`、`revive`、`forbidigo`、`gofmt`、`goimports`。
- `forbidigo` 禁止的模式（**硬性**）：
  - `log.Fatal` / `log.Fatalf` / `log.Fatalln` / `os.Exit`（库代码内）
  - `panic(`（除 init 期断言与明确标注的不可恢复场景）
  - `unsafe.`（除非在经评审的白名单文件里）
  - `pkg/errors`（不引入；错误包装一律用标准库）
  - `time.Sleep`（应用层禁止；改用 timer + ctx）
  - `http.Get` / `http.Post`（必须用带超时的自定义 client，见 F-59）
- 禁止 `init()` 里做 I/O 或启动 goroutine（用显式 `New()`）。
- CI 中 lint 必须为**阻断性**。

**边界**
- 白名单文件必须集中在一个**固定目录** `internal/unsafeutil/`，并在注释里写明理由。
- 规则变更需在 PR 描述中说明。

**验收**
- 故意提交 `log.Fatal` 的代码，CI lint 失败并指出位置。
- 故意提交 `http.Get` 的代码，CI lint 失败。

---

### F-74 黄金测试 · P1

**价值**：提示词与权限表是最容易被无意改坏、又最缺测试的资产。

**规格**
- 对以下产物做逐字节黄金比对：`Policy.Render(role)`（每个角色一份）、`RenderPrompt(样例数据)`（每个模板一份）、工具 schema 列表。
- 期望值存 `testdata/*.golden` 文件，**不硬编码在测试代码里**。
- 提供 `-update` 标志一键重写 golden 文件。
- 比对前统一行尾（`\r\n` → `\n`）与去尾空白（**这条是硬性要求**，否则 Windows 检出即红）。
- 时间等非确定字段用**固定 clock 注入**，而不是"跳过某一行"。

**边界**
- golden 文件必须纳入版本控制。
- `-update` 必须拒绝在 CI 环境执行（防止悄悄改期望值）。

**验收**
- 在 LF 与 CRLF 两种检出下测试均通过。
- 修改一个权限项后测试失败并指出差异行。
- `-update` 能正确重写文件。

**易错点**
- 把期望值硬编码在测试代码里（双份维护）且不处理 CRLF，在 Windows 上直接失败。

---

### F-75 契约测试 · P0

**价值**：有些错误只有在"多轮协议交互"里才暴露，单测覆盖不到。

**规格**
- **多轮工具调用契约**：用 httptest mock server 记录请求体，断言第二轮请求中 assistant 消息含 `tool_calls`（含 id/name/arguments），tool 消息含匹配的 `tool_call_id`。（这里最容易出错：把 tool call 塞进 content，第二轮必被 400。）
- **流式契约**：mock SSE 流（含分片 tool_calls），断言聚合结果正确、ctx 取消后连接关闭。
- **一个动作"端到端"契约**：从"注入一条群消息"到"断言 Caller 收到的 API 请求"，全链路无网络。
- **传输契约**：Driver 的 Connect/Listen/cancel 行为（用内存管道）。
- **裁剪契约**：历史裁剪后不存在孤立的 tool 结果消息。

**边界**
- mock server 必须能模拟错误（4xx/5xx/超时/中途断流）。
- 契约测试必须能在 CI 中离线运行（不依赖真实 API key）。

**验收**
- 上述 5 类契约测试全部存在且通过。
- 故意破坏 tool_call_id 传递后契约测试失败。

---

### F-76 手写 Fake · P0

**价值**：无需 mock 框架即可做确定性测试，且 fake 本身就是"接口是否好用"的第一手反馈。

**规格**
- 必须提供的 fake：`FakeLLM`（脚本化响应序列 + 调用记录）、`FakeCaller`（记录所有 API 请求）、`FakeClock`（可控时间）、`FakeStore`（内存 KV）、`FakeDriver`（内存事件管道）。
- `FakeLLM` 支持：按顺序返回预设响应、注入延迟、注入错误、断言调用次数与最后一次请求内容。
- 所有 fake 与被 fake 的接口用编译期断言绑定（`var _ LLM = (*FakeLLM)(nil)`）。

**边界**
- fake 必须并发安全（测试里常并发调用）。
- fake 的断言失败信息要包含"实际 vs 期望"的完整内容，便于排查。

**验收**
- 核心链路的单元测试**不需要网络、不需要 sleep**（除显式并发测试）。

---

### F-77 基准测试 · P1

**价值**：明确指出"哪里不能慢"，防止性能悄悄退化。

**规格**
- 必测：`BenchmarkRouteMatch`（1000 路由）、`BenchmarkBind`（状态绑定）、`BenchmarkRenderPrompt`（提示词渲染）、`BenchmarkPolicyAllow`（权限判定）、`BenchmarkParseMessage`（含 CQ 字符串）、`BenchmarkACReplace`（1 万词表）、`BenchmarkHammingSearch`（1 万向量）。
- 每个基准必须 `b.ReportAllocs()`，关注分配次数。
- 提供 `make bench-compare`：与上次结果对比，回退超过 20% 则告警。

**边界**
- 基准测试不得依赖网络与磁盘。
- 结果需在 CI 中归档（便于趋势对比）。

**验收**
- 上述基准全部存在并能运行。
- 至少有一个基准设定了明确的性能预算并在 CI 中校验。

---

### F-78 CI 流水线 · P0

**价值**：把上述所有纪律变成自动化门禁。

**规格**
- 触发：push 与 pull_request。
- Job 1（必过）：`go build ./...` → `go vet ./...` → `golangci-lint run` → `go test -race ./...`（**-race 是硬性要求**）。
- Job 2：黄金测试（F-74）在 Linux 与 Windows **两个平台**跑（专治 CRLF 类问题）。
- Job 3：`go test -run=XXX -bench=. -benchtime=1x` 冒烟 + 覆盖率报告（覆盖率门槛可设，默认 60% 且不许下降）。
- 工具链版本**必须与 go.mod 一致**（禁止 CI 用 go 1.20 构建声明 go 1.26 的模块）。
- Actions 版本**固定到具体 tag/SHA**（不用 `master`）。
- 可选：`goleak` 检查、依赖漏洞扫描（`govulncheck`）。

**边界**
- 失败必须给出可操作的信息（哪个文件哪一行）。
- 缓存 go module 与 build cache 以加速。

**验收**
- 故意引入 race 的代码使 CI 失败。
- CI 上的 Go 版本与 go.mod 声明一致（脚本断言）。

---

### F-79 接口断言与文档同步 · P1

**价值**：防止"接口改了但实现没跟上"和"文档描述了不存在的功能"。

**规格**
- 每个接口的每个实现都写 `var _ Iface = (*Impl)(nil)`。
- 每个对外声明的能力必须在 `main` 的装配路径上真实可达（**"未接线"是首要反模式**）；装配在启动日志中打印"已启用能力清单"。
- 若某包暂不接线，必须放到 `_experimental/` 目录（Go 工具链忽略下划线开头目录）并在 README 标注。
- 文档中引用的配置项、指标名、环境变量，必须能在代码中 grep 到（CI 可加一条检查脚本）。
- 公开 API 变更需更新 `CHANGELOG`。

**边界**
- 断言应放在实现文件而非测试文件（编译期即生效）。

**验收**
- 删除某个实现的方法后编译失败。
- 列出所有环境变量与文档中声明的一致（脚本校验）。
- 启动日志中能看到"已启用能力清单"。

---

## 12. 明确不做（反模式黑名单）

以下做法在实践中被证明有害，AgentBot **明确禁止**：

| # | 禁止项 | 原因 |
|---:|---|---|
| 1 | 全局包级可变状态（路由表、配置、账号表） | 无法多实例、无法并行测试、测试互相污染 |
| 2 | `log.Fatal` / `os.Exit` / `panic` 出现在请求路径 | 一条坏输入即可打崩整个进程 |
| 3 | 库内 `init()` 做 I/O、启动 goroutine、加载依赖 | 不可控的副作用，测试无法隔离 |
| 4 | 用 `time.Timer` 做超时且不取消已启动的 goroutine | 超时后 goroutine 仍写共享状态 → 数据竞争 |
| 5 | 所有 goroutine 不接受 `context.Context` | 无法取消、无法优雅关闭、泄漏 |
| 6 | 结构体字段写具体实现类型（`*OpenAIClient`） | 抽象在调用点失效，无法替换/mock |
| 7 | 定义接口但无第二个实现、且不进装配路径 | "未接线"代码即负债；会误导读者 |
| 8 | 定义字段但从不写入（如 `Metadata`） | 死字段，调用方读了永远拿不到 |
| 9 | 并行数组分开排序（`texts` 与 `ids`） | 必然错位；必须合并成结构体切片 |
| 10 | 用字节数当 token 数 | 上下文预算严重失真 |
| 11 | 用"0 表示未设置" | 无法表达显式零值 |
| 12 | 提示词用位置参数（`%v` × N） | 顺序错不报错；改用命名占位 |
| 13 | 提示词模板/权限表不做行尾归一化 | Windows 检出即失败 |
| 14 | 把 tool call 塞进 `content`、消息转换丢掉 `tool_call_id` | 违反 OpenAI 协议，第二轮必被拒绝 |
| 15 | 不用 `UseNumber()` 解析含大整数的 JSON | int64 精度静默丢失 |
| 16 | 出站 HTTP 用默认 client（无超时、无大小限制） | SSRF + 内存放大 |
| 17 | 图像解码后再缩放 | 挡不住超大图片（内存放大） |
| 18 | 失败只记 debug 日志后 continue（无用户可见反馈、无错误出口） | 生产环境完全静默，问题无法定位 |
| 19 | 硬编码密钥、日志打印完整密钥 | 安全事故 |
| 20 | 复制粘贴多平台分支 | 同一 bug 要改 N 处 |
| 21 | 无界增长的 map / 无 TTL 的缓存 | 长时间运行必然 OOM |
| 22 | 用 finalizer 做资源回收 | 时机不确定，不可作为正常路径 |
| 23 | 依赖代码生成但没有生成器/`go:generate` | 无法复现，改动即手工同步 |
| 24 | CI 工具链版本与 `go.mod` 不一致 | "本地能过、CI 不能复现" |
| 25 | 部署清单（env 名、探针路径、指标端点）与代码各自定义 | 契约断裂，上线才发现 |

---

### 12.1 明确不做（范围外）

以下能力**不在 AgentBot 范围内**。它们有的是别处的历史包袱，有的与已有 Feature 语义重叠；不要以"顺手实现"为名引入。

| # | 不做的事 | 理由 |
|---:|---|---|
| 1 | 剧本引擎（YAML 声明的分支剧情与轮次控制流） | 与 F-16 Await/Stream + F-35/F-36/F-37 语义重叠；多轮交互统一走 Await |
| 2 | 声明式消息链 DSL（段序列模板匹配） | F-14 内置规则 + F-01 的 `Event.Get(path)` 已覆盖，DSL 只是额外维护面 |
| 3 | conversationID → UserInfo 绑定注册表 | `SessionKey`（F-21）已含 SelfID/GroupID/UserID，不需要第二套身份映射 |
| 4 | K8s / Helm 部署清单 | 单二进制 + 最小 Dockerfile 已足够；清单先行必然契约断裂 |
| 5 | Makefile | CI 与 Go 原生命令足够，避免第二套构建入口 |
| 6 | 代码生成 | 见反模式 #23；一切手写且可复现 |
| 7 | lint 自动修复（`issues.fix: true`） | CI 只报不改，避免流水线悄悄改写源码 |
| 8 | 消息撤回与"触发消息 → 发送消息"的 TTL 映射 | F-05 只记录发出的消息 ID；撤回不属首版 |
| 9 | 语种过滤与随机触发概率 | 无明确需求；F-57 只保留 `Verdict.Score` 供外部打分接入 |
| 10 | LLM 装饰器里的面客错误话术回调 | 面客文案统一归 F-55 出口层，装饰器不应感知话术 |
| 11 | 背压 ring buffer（覆盖最旧事件） | F-20 用 `chan Event` + 固定 worker，语义更简单 |
| 12 | 发布自动化（打 tag + Release 产物） | M0–M3 只做 CI；发版流程在 M3 之后单独设计 |

## 附录 A：里程碑建议

| 里程碑 | 目标 | 包含 Feature | 前置依赖 | 完成判据 |
|---|---|---|---|---|
| **M0 · 骨架** | 立规矩，一行业务代码都不写 | F-73, F-76, F-78, F-25, F-67, F-70（仅骨架） | 无 | CI 跑通 build/vet/lint/test -race；**空 Bot** 能优雅关闭且 goleak 干净；无 lint 违规 |
| **M1 · 最小闭环** | "群消息 → LLM → 回消息"跑通 | F-01~F-06, F-08~F-14, F-21, F-26, F-28, F-30, F-33, F-34, F-38, F-39, F-53, F-55, F-59, F-80, F-81 | M0；F-38 默认只实现 `Window` | F-75 中"流式契约"与"单动作端到端"两条通过；真实群里能对话 |
| **M2 · 交互与工具** | 多轮交互 + 工具调用 | F-15, F-16, F-35, F-40, F-41~F-45 | M1；**F-15 必须先于 F-16** | Await 多轮对话可用；F-75 中"多轮工具调用契约"通过 |
| **M3 · 生产可用** | 可以开放给真实用户 | F-07, F-17, F-18, F-19, F-22, F-23, F-24, F-27, F-29, F-31, F-32, F-47, F-48, F-50, F-54, F-56, F-57, F-58, F-60, F-61, F-64, F-66, F-68, F-69, F-74, F-75（其余）, F-77, F-79, F-82, **F-83~F-89** | M1 | 限速/开关/审计/指标/探针全就绪；`-race` 与 goleak 干净 |
| **M4 · 增强** | 降本与高级能力 | F-20, F-36, F-37, F-46, F-49, F-51, F-52, F-62, F-63, F-65, F-71, F-72 | M3 | 成本下降可量化；按需启用 |

> **"前置依赖"列的含义**：某个里程碑的功能若依赖更晚里程碑的功能，就必须在该里程碑内先落地**最小可用版本**（例如 M0 的 F-70 只做空 Bot 的关闭骨架，M1 的 F-38 只实现 `Window`）。不允许"按里程碑排期"变成"先写接口、永不接线"。

> **P0 不等于都在 M0/M1**：P0 描述"首版必要性"，里程碑描述"落地顺序"。所以 F-15/F-16/F-35/F-41/F-42（在 M2）与 F-47/F-54/F-75（在 M3）虽为 P0，仍排在最小闭环之后——这是顺序选择，不是矛盾。

**建议节奏**：M0 与 M1 之间的纪律不可跳过——绝大多数严重问题（全局态、无 ctx、无 race 检查）都源于"先跑起来再说"。

---

## 附录 B：Feature 与反模式的对应关系

| 反模式 | 由哪些 Feature 规避 |
|---|---|
| 全局可变状态 | F-08（实例化路由）、F-21（Session 归属状态）、F-25（配置校验） |
| 进程被打崩 | F-04/F-13/F-35（recover + error 传播）、F-56（构建期拒绝） |
| 数据竞争 | F-11（每事件独立 State）、F-12（快照）、F-17（defer 释放）、F-36（反思状态属单次 Run） |
| goroutine 泄漏 | F-16（超时注销）、F-23（WaitGroup + Shutdown）、F-28（ctx 感知发送）、F-70（goleak 验证） |
| 交互式多轮难写 | F-16（Await/Stream） |
| 提示词易改坏 | F-33（模板引擎）、F-74（黄金测试）、F-65（前缀稳定） |
| 权限越界 | F-53（输入侧约束）、F-54（执行侧拦截）、F-45（审批） |
| 成本失控 | F-32（预算）、F-62/F-63（缓存）、F-66（配额） |
| 协议错误 | F-29（分片聚合）、F-35（tool 消息契约）、F-75（契约测试） |
| 静默失败 | F-25（启动期校验）、F-60（审计）、F-67（结构化日志）、F-68（指标） |
| 文档与代码漂移 | F-79（文档同步检查）、F-74（黄金测试） |
| 部署契约断裂 | F-68/F-69（指标与探针常量单一来源）、F-25（校验） |

---

*文档版本：v2 · 共 89 条 Feature（P0 42 / P1 34 / P2 13）+ 25 条反模式禁止项 + 5 个里程碑*
