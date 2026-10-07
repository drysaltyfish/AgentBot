# AgentBot

跑在 QQ 上的自用 AI 机器人。它把群聊和私聊消息交给大模型，回复时带上长期记忆、工具调用和人格设定。

部署形态刻意做得很简单：**一个可执行文件 + 一份 YAML + 一个数据目录**。除了 OneBot 平台和模型端点，
不需要数据库服务端、Redis、消息队列或容器编排。

- 群聊默认只在被 **@** 时回复；没被 @ 的消息仍会作为背景进入上下文，只是受单独的 token 预算约束。
- 所有出站消息走同一个出口，在一处完成内容过滤与审计。
- 配置写错、密钥缺失、数据库打不开，都会让启动**失败**——不带着问题运行。

AgentBot **不是**通用聊天框架、多租户服务，也没有 Web UI。

> **当前状态**：功能规范 [FEATURES.md](FEATURES.md) 共 **89 条**，已实现 **87 条**。
> 未实现的两条是「多账号路由」与「多供应商路由」，都在设计目标之外。
>
> **计数口径要说清**：这里的"已实现"按 **Feature** 计，判据是它的**验收**通过。
> 个别 Feature 的规格里列了多于验收所要求的东西，那部分可能没做——
> 最典型的是 F-04：验收只要求"内存 mock 传输能跑通"，已通过；但它规格里列的
> `wsserver`（反向 WS）与 `http`（HTTP 上报）两个内置驱动**并未实现**，
> 配置它们会在启动前被**直接拒绝**（见[局限与已知偏离](#7-局限与已知偏离)）。
> 因此**不要用这个数字推断某个具体配置项可用**，配置项的权威参考是
> [config.example.yaml](config.example.yaml) 与启动期校验。
>
> 其余已知偏离集中在本篇末尾的[局限与已知偏离](#7-局限与已知偏离)。

## 目录

- [1. 五分钟跑起来](#1-五分钟跑起来)
- [2. 它能做什么](#2-它能做什么)
- [3. 配置](#3-配置)
- [4. 运行与运维](#4-运行与运维)
- [5. 架构](#5-架构)
- [6. 开发](#6-开发)
- [7. 局限与已知偏离](#7-局限与已知偏离)
- [8. 文档索引](#8-文档索引)

---

## 1. 五分钟跑起来

### 前置条件

| 需要什么 | 说明 |
|---|---|
| Go **1.27.1** | `go.mod`、CI、`.golangci.yml` 三处锁定同一版本 |
| 一个 OneBot v11 实现 | 例如 NapCat / Lagrange，暴露一个 WebSocket 地址 |
| 一个 OpenAI 兼容的模型端点 | 默认按 DeepSeek 配置；想先不接真模型可以用 `provider: echo` |

### 步骤

```sh
git clone git@github.com:drysaltyfish/AgentBot.git
cd AgentBot

# 1) 准备配置
cp config.example.yaml config.yaml

# 2) 至少改这三处
#    llm.api_key 或 llm.api_key_env  —— 模型密钥
#    transport.url                    —— OneBot 的上报地址，如 ws://127.0.0.1:3001
#    transport.self_id                —— 机器人自己的 QQ 号

# 3) 校验配置：打印脱敏后的生效配置后退出，不启动服务
go run ./cmd/server --config config.yaml --check-config

# 4) 启动
go run ./cmd/server --config config.yaml
```

不想接 QQ 就想看看它长什么样，把 `llm.provider` 改成 `echo`：那是一个假模型，回复固定模板、
完全不访问网络。

启动成功的三个信号：

1. 启动日志里有一份 `capabilities` 清单，列出本次**真实装配**启用的能力；
2. `curl http://127.0.0.1:9090/healthz` 返回正常；
3. 给机器人发一条消息，它能回。

### 构建与最小部署

```sh
go build -o bin/agentbot ./cmd/server
```

运行时只需要三样东西：

```
agentbot        # 可执行文件
config.yaml     # 唯一配置
data/           # 数据目录，首次启动自动创建
```

systemd 的要点只有一个：设好 `WorkingDirectory`，让配置里的相对路径落在同一个目录下。

```ini
[Unit]
Description=AgentBot
After=network-online.target

[Service]
WorkingDirectory=/opt/agentbot
ExecStart=/opt/agentbot/agentbot --config /opt/agentbot/config.yaml
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

启动顺序是**先校验、后监听**：配置非法、密钥缺失、持久层打不开、人格或权限表有问题，都会以
非零退出码失败，不会带病运行。

---

## 2. 它能做什么

这一节按「你能感知到的行为」来讲。每个开关的配置写法见第 3 节。

### 2.1 聊天与回复

- **私聊和群聊是两套策略**：私聊可设 `always` / `never`；群聊可设 `always` / `on_mention` / `never`。
  群聊默认 `on_mention`，不 @ 就不回复，避免刷屏。
- **没被 @ 的消息也是上下文**：它们不会触发回复，但会作为背景进入模型上下文，并按单独的
  token 预算压缩——群友刷屏不会把真正的对话挤出窗口。
- **分段发送**：回复里的空行会拆成多条消息发出（真人也是一条一条发的），带连发间隔和条数上限，
  超出部分合并进最后一条。
- **流式增量发送**（可选，默认关闭）：在直连模型路径下边生成边发送；ReAct 路径会自动退回整段发送。
- **单飞反并发**（可选，默认关闭）：同一用户连点两次时，第二次在入口就被拒，避免两次昂贵调用。

### 2.2 记忆

- **分层记忆**：Working（当前工作集）/ Episodic（情节）/ Semantic（语义事实）三层。
- **两条写入通道**，可以同时开：
  - **规则触发**：用户说「记住：xxx」就无条件写入——确定、不花模型调用、可审计；
  - **模型主动**：在系统提示词末尾追加指令，让模型自己判断什么值得长期记住（概率性，可能漏记）。
- **语义判官**：写入去重不靠纯字符相似度——「旧的一条」和「新的一条」相似度正好 0.50，却是两件
  不同的事。所以完全相同的、相似度 ≥ 0.90 的、相似度 < 0.30 的都走确定性判定；只有落在歧义带的
  才用关闭思考的模型问一次，结果按文本对缓存。判官不可用时退回确定性判据，**写入照常成功**。
- **记忆按会话作用域隔离**，跨用户不串台。群聊里记忆按群共享（同群需要共同上下文），但每条事实带
  **归属人**，所以「张三怕辣」不会被答成李四的事；团建通知这类**公共记忆**没有归属，谁查都看得到。
- **空闲反思**（可选，默认关闭）：对话滑出上下文窗口、且会话安静下来之后，自动把那段内容提炼成
  长期记忆。它会在没人说话时花钱，所以有四道闸门（静默 5 分钟、有新内容、同会话 30 分钟节流、
  每日 50 次硬上限）和关闭思考的模型。
- **遗忘、检视、导出**：可遗忘单条、列出当前作用域的记忆，或用 `--export-memories` 全量导出 JSONL。

### 2.3 工具调用

内置 10 个工具：

| 工具 | 作用 |
|---|---|
| `calculator` | 四则运算与常见数学表达式 |
| `current_time` | 当前时间（可用配置的时区） |
| `json_query` | 在 JSON 文本里按路径取值 |
| `http_fetch` | 抓取 URL（走安全客户端：白名单、体积上限、超时） |
| `get_user_info` | 查询群成员资料（需要平台 API） |
| `memory_save` | 保存一条长期记忆 |
| `memory_recall` | 召回当前作用域的记忆 |
| `recall_history` | 按关键词回溯当前会话历史，含命中处的前后文 |
| `forget_memory` | 遗忘指定记忆 |
| `list_memories` | 列出当前作用域的记忆 |

- **按需注册**：`agent.tools` 留空 = 全部注册；写了名单 = 只注册这些，且顺序恒定（顺序稳定才能
  保住提示词前缀缓存）。
- **权限与人工审批**（可选，默认关闭）：按「工具 × 角色」决定放行 / 需审批 / 拒绝；需要审批却没有
  审批通道时按拒绝处理。
- **沙箱**（可选，默认关闭）：生效的键是 `forbidden_tools`（按工具名禁止）、`max_output_bytes`
  （输出截断）、`require_read_only`（只允许只读工具），以及联网闸门 `network_tools` / `allow_network`。
  **注意**：`http_fetch` 已经声明自己需要联网，所以开启沙箱后它**默认会被拒绝**——要么把它写进
  `sandbox.network_tools: [http_fetch]`，要么开 `allow_network: true`。启动日志会把这件事直接说出来。
  `read_roots` / `write_roots` / `env_allowlist` / `forbidden_ops` 四个键仍然空转：它们要等出现真正
  接收路径/环境变量/命令参数的工具才会生效，而现有 10 个内置工具都不是（见[局限与已知偏离](#7-局限与已知偏离)）。

### 2.4 人格

- **目录式定义**：每个 `<name>.yml` 一个人格，名字必须和文件名一致。启动期校验引用，写错人格名
  在启动时就会失败。
- **会话级切换**：`/persona` 切换当前会话的人格，结果落 SQLite，重启后保持。
- **和缓存的关系**：人格只改变提示词的**半静态段**，所以不同人格的会话仍然共享同一段静态前缀缓存。

### 2.5 历史与召回

- **呈现窗口和存储保留量是分开的**：存储默认保留 400 条，回灌给模型的窗口默认只有 20 条。这样
  `recall_history` 才有窗口之外的内容可召回。
- **高水位批量裁剪**：窗口成批滑动，而不是每轮滑一点——逐轮滑动会让请求前缀每轮都变，缓存必然失效。
- **混合检索**（可选，默认关闭）：`recall_history` 改用「BM25 关键词 + 二值向量 + RRF 融合」，
  专有名词和改写过的表达都能命中。
- **摘要树**（可选，默认关闭）：独立于关键词/向量的第三条召回源，在摘要层检索，宏观问题可以命中
  上层摘要。

### 2.6 管理命令与权限

聊天内命令：

| 命令 | 作用 | 谁能用 |
|---|---|---|
| `/help` | 列出全部可用管理命令 | 超管 |
| `/switch <plugin> on\|off` | 按群开关某个插件，落盘并重启保持 | 群 owner / admin |
| `/ban <用户号> [原因]` | 永久封禁 | 超管 |
| `/unban <用户号>` | 解除封禁 | 超管 |
| `/banlist` | 列出当前封禁 | 超管 |
| `/cost` | 今日 / 累计 / 本会话的调用次数与费用 | 超管 |
| `/persona <名称>` | 切换当前会话人格 | 超管 |
| `/prompt-hash` | 打印提示词各段哈希（静态 / 半静态 / 动态） | 超管 |

每一次调用——无论通过与否——都会进审计。

**名单**（`access`，默认关闭）决定「谁可以被处理」，按 QQ 号和群号配置：

- `allow` 是白名单（只有名单内处理），`deny` 是黑名单（名单内丢弃）。
- 命中即在**路由层直接丢弃**：不匹配路由、不建会话、不落库、不产生模型调用。
- 名单只看得到 QQ 号/群号，这是它最彻底的地方；需要看消息内容（敏感词等）是 `moderation` 的事，
  两者可以同时开。
- 超管默认绕过名单，免得「白名单配错把自己锁在门外」——那种情况只能改文件重启。

**角色**（`access.roles`）用 QQ 号直接指定，优先于平台上报的群成员角色；同一个 QQ 写进多个角色时
按权限取高。角色能力对照：

| 角色 | 管理命令 | 权限表 | 其他 |
|---|---|---|---|
| `superuser` | 全部 | 内置表里权限最高 | 永不封禁、绕过名单 |
| `owner` | `/switch` | 内置表里次高 | 平台上报的群主也是这一档 |
| `admin` | `/switch` | 内置表：不能禁言 | 平台上报的群管理员也是这一档 |
| `member` | — | 内置表：只有 `send_msg` | 默认档 |

> 超管**只有** `access.roles.superuser` 这一个位置。旧键 `moderation.super_users` 已移除，
> 出现即启动失败并给出迁移指引。

### 2.7 群管理与防刷

- **入站内容审查**（可选）：敏感词命中后按 `mask`（脱敏放行）或 `block`（拦截）处理；词表支持内联 +
  文件（每行一个词，`#` 注释），并且**热加载**——新词表编译失败时保留旧表。
- **黑名单与防刷**（可选）：封禁用户/群；窗口内超阈值或连续重复消息触发临时封禁；超管永不封禁。
- **限速**（可选，默认关闭）：按用户和群做令牌桶限速，超限事件被**整条丢弃**（连「只记录」的兜底
  路由也不执行），参数**热加载**。
- **功能开关**：`/switch` 按群隔离，状态落盘。

### 2.8 成本与缓存

- **成本统计与配额**（可选）：按模型返回的真实 usage 记账，支持 global / session / user 三个维度和
  day / month / total 三个周期；软限告警，硬限在**调用之前**拒绝（拒绝发生在花钱之前）。
- **语义缓存**（可选，默认关闭）：常见问题命中缓存可直接作答，零 token。命中条件是「问题相似度达标
  **且** 上下文指纹（人格 / 系统提示词）一致」；带工具调用的轮次不进缓存。
- **前缀缓存友好**：提示词前缀逐字节稳定，命中率可以在 `--stats` 和指标里查到。提示词快照记录每轮
  指纹，并区分「记忆变更（预期）」和「前缀意外分歧（告警）」。

### 2.9 可观测性

- **结构化日志**：JSON 行，带组件标签和 `trace_id`；默认只记长度和摘要，`debug_content` 打开才记正文。
- **指标**：`GET /metrics` 直接抓，上述开关性能力都有对应的计数/耗时指标。
- **审计**：管理员命令、封禁、审查拦截、审批等待、出站消息等安全事件落 JSONL，**不可关闭**。
- **分布式追踪**：入站事件建立 W3C trace 上下文，日志和出站 `traceparent` 头一致；采样默认 1%
  （传播不受采样影响）。

### 2.10 稳定性与安全

- **优雅关闭**：收到信号后按预算关闭组件（默认 10s），期间不再接新事件，在途的等待会被持久化。
- **在途恢复**：交互式等待与待审批落 SQLite，重启后至少能通知原会话，而不是让用户一直干等。
- **密钥管理**：`api_key_env` > `api_key_file` > `api_key`（内联会在启动时告警）；`${VAR}` 引用的
  环境变量未设置即启动失败。
- **传输鉴权**：`wsclient` 模式支持 `access_token`、签名校验和 IP 白名单。
  **`wsserver` / `http` 两种入站模式尚未实现**，配置它们会被直接拒绝（见「局限与已知偏离」）。
- **出站 HTTP 安全**：统一客户端（白名单、超时、体积上限、重定向策略），不允许裸调用。
- **权限双层**：提示词里按角色渲染权限表（输入侧约束），平台 API 出口再按角色硬拦截（执行侧）。

---

## 3. 配置

一份 YAML。**完整字段、默认值和逐项注释见 [config.example.yaml](config.example.yaml)**，
它是配置的权威参考；schema 定义在 `internal/config/sections.go`，CI 有测试保证示例里的每个键
都在 schema 中存在。

本节只讲三件事：通用约定、最常改的片段，以及**打开后会改变行为的开关**。

### 3.1 通用约定

| 约定 | 含义 |
|---|---|
| 未设置 vs 零值 | 可选字段用指针表达；`0` 和空串是明确的值，不等于「未设置」 |
| 时长 | Go 风格字符串：`10s`、`1500ms`；负数直接报错 |
| 密钥引用 | `${VAR}` 可用于 `transport.access_token`、`transport.signature_secret`、`llm.api_key` |
| 校验时机 | 启动时全部校验，**一次性报告全部问题**；`--check-config` 打印脱敏后的生效配置 |
| 默认值 | 不写就是默认值；示例配置里的值不等于默认值 |

### 3.2 最常改的片段

**设超管和名单**（只处理指定的群和人，其余消息在路由层直接丢弃）：

```yaml
access:
  enabled: true
  mode: allow                 # allow = 白名单；deny = 黑名单
  users: [10001, 10002]       # 允许的 QQ 号
  groups: [123456789]         # 允许的群号
  check_users_in_group: true  # 群里也要求人在名单里
  roles:
    superuser: [10001]        # 超管：可用全部管理命令、永不封禁、绕过名单
    admin: [10002]            # 可用 /switch
```

**换人格**，把提示词正文放进 `prompts/` 下的文件：

```yaml
llm:
  system_prompt_file: prompts/your-persona.md   # 优先级高于内联的 system_prompt
```

`system_prompt(_file)` 在**启动时读一次并固定**，刻意不热加载：运行时读文件会让前缀在运行中变化，
前缀缓存全部失效。写提示词的两条实测经验见 [prompts/README.md](prompts/README.md)。

### 3.3 会改变行为的开关

下面这些分节**默认全部关闭**。打开它们会改变机器人的对外行为，不只是多一项统计——建议一次只开一个。

| 分节 | 打开后会怎样 |
|---|---|
| `access` | 名单外的消息在路由层直接丢弃 |
| `moderation` | 入站消息按敏感词脱敏或拦截；黑名单与防刷生效 |
| `ratelimit` | 超限消息被整条丢弃，连兜底路由也不执行 |
| `singleflight` | 同一用户/会话的并发第二次请求被拒 |
| `stream` | 直连模型路径下边生成边发送 |
| `semcache` | 相同问题可能直接命中缓存作答（零 token） |
| `retrieval` | `recall_history` 改用混合检索，并可加摘要树 |
| `cost` | 按价格表记费用，按配额在调用前拒绝 |
| `sandbox` | 禁止指定工具、截断输出、只允许只读工具；**开启后 `http_fetch` 默认被拒**（需列进 `network_tools` 或开 `allow_network`） |
| `toggle` | `/switch` 按群开关生效并落盘 |
| `agent` | 回复走 ReAct 循环，可调用工具（关闭则直连模型） |
| `agent.reflect` | 会话安静后自动提炼长期记忆（会在没人说话时花钱） |

另外几条值得记住的配置语义：

- **`policy.file` 是覆盖语义**：文件不存在就用内置默认表并记一条日志；文件存在但解析/校验失败则
  **启动失败**——权限表是安全边界，静默退回默认等于换了一套权限。
- **`history.file` 和 `agent.memory_file` 现在只是一次性导入源**：数据一直在 SQLite 里，这两个路径
  用来把旧版 JSONL 迁移进库（导入幂等）。留空就不做导入。
- **热加载的资产**：敏感词表、人格目录、权限表、限速参数。**提示词正文与模板刻意不热加载**，
  启动时固定正是前缀缓存的前提。
- **`llm.max_iterations` 是空转字段**：它会被校验，但全仓库没有消费方；工具调用轮数由
  `agent.max_iterations` 控制。

---

## 4. 运行与运维

### 4.1 命令行

所有子命令都是**一次性维护动作**：执行完打印结果并退出。

| 参数 | 说明 |
|---|---|
| `--config <path>` | 配置文件路径，默认 `config.yaml` |
| `--check-config` | 只校验配置并打印脱敏后的生效配置，然后退出 |
| `--selftest <QQ>` | 连接平台 → `get_login_info` → 给该 QQ 发一条自检消息 → 退出。用于打通整条链路 |
| `--stats` | 打印用量台账：消息数、请求数、工具调用数、token、前缀缓存命中率、估算成本、花费最高的会话 |
| `--export-memories <path>` | 把**全部**记忆导出为 JSONL 后退出（跨作用域，所以是维护命令而非会话内能力） |

### 4.2 运维端点

`ops` 默认启用，只绑回环（`127.0.0.1:9090`）——这也是默认不配鉴权也安全的前提。

| 端点 | 用途 |
|---|---|
| `GET /metrics` | Prometheus 文本格式指标：请求、延迟、token、缓存命中、成本、限流拦截、审查拦截等 |
| `GET /healthz` | 进程存活（不查依赖） |
| `GET /readyz` | 依赖就绪（存储等），结果按 `ready_cache_ttl` 缓存 |

配了 `auth_token` 之后三个端点都要求 `Authorization: Bearer <token>`。

### 4.3 运行时数据文件

| 路径（示例配置里的写法） | 内容 | 不配会怎样 |
|---|---|---|
| `data/agentbot.db` | 唯一 SQLite（WAL）：消息归档、会话台账、长期记忆、在途操作、提示词快照 | 不配也落到 `data/agentbot.db` |
| `data/agentbot.db-wal` / `-shm` | WAL 附属文件 | SQLite 正常产物 |
| `data/history.jsonl` | 旧版历史 | 默认为空 = 不做导入；数据本来就一直在 SQLite 里 |
| `data/memory.jsonl` | 旧版记忆 | 默认为空 = 不做导入 |
| `data/audit.jsonl` | 审计日志（只追加，不可关闭） | 不配也写这个路径；显式设为空则只写 stdout |
| `data/toggles.json` | 功能开关状态 | 不配也写这个路径，`/switch` 重启后保持 |
| `data/blacklist.json` | 黑名单与临时封禁 | **默认为空 = 仅进程内，重启即丢**；要让 `/ban` 保持必须显式配 |

### 4.4 启动失败排查

| 现象 | 原因 |
|---|---|
| `transport.url 必须提供` | `mode=wsclient` 却没填上报地址 |
| `transport.mode 尚未实现` | 填了 `wsserver` / `http`；这两种入站驱动没有实现，只会让进程带着空 URL 无限重连 |
| `llm.model 必填` | 没配模型名 |
| `环境变量 XXX 未设置` | 某个 `${VAR}` 指向了不存在的环境变量（刻意的：静默变空串比启动失败更难查） |
| 权限表 / 人格 / 敏感词正则非法 | fail-fast：安全与提示词资产配错不允许带病启动 |
| 打开数据库失败 | `store.path` 不可写或磁盘损坏；不会降级为内存 |
| 名单模式未写 / `allow` 模式名单为空 | fail-fast：防止「以为开了其实没开」或「把所有人挡在门外」 |
| `unsupported llm.provider` | `provider` 只支持 `echo` / `openai` / `deepseek` |

### 4.5 排障入口

- **结构化日志**：JSON 行，带 `trace_id`；命中出站请求时会发出 `traceparent` 头，可以把日志和平台侧
  链路对上。
- **启动能力清单**：启动日志里的 `capabilities` 字段是本次装配**真实启用**的能力，不是一份固定清单——
  它由装配事实推出，所以「库写完了但没接上」不会显示成已启用。

---

## 5. 架构

这一节是给要改代码的人的。只想部署使用的话，读到第 4 节就够了。

### 5.1 分层与依赖方向

```
cmd/server   组合根：读配置 → 装配 → 启动 → 关闭
             （唯一允许 import 全部 internal 包的地方）
   │
internal/*   库：只依赖比它更基础的库，不反向依赖 cmd/server
   │
stdlib + 少数白名单依赖（SQLite driver、yaml、websocket）
```

约定：除 `cmd/server` 外全部放 `internal/`；不在装配路径上的包不进仓库；`unsafe` 只在
`internal/unsafeutil` 白名单里出现。

### 5.2 一条消息的旅程

```
OneBot 平台
   │ WebSocket 帧
   ▼
transport.WSClient ──► sink 读循环（这里禁止调用平台 API）
   │
   ▼
router.Engine.Dispatch（pre / mid / post 三段钩子；名单、审查、限速挂在这里）
   │
   ├── reply 规则路由（命中即回复）
   └── Always 兜底路由（只记录）
   │  入队（有界；满则丢弃并计数）
   ▼
reply worker 池（4 个 goroutine）
   │
   ▼
reply.Pipeline.Handle
  自动记忆 → 引用解析 → 记录用户轮次 → agent.Run → 用量台账 → 快照 → 分段/流式发送
   │
   ▼
agent.ReactAgent（ReAct 循环 + 工具）
   │
   ▼
outbound.Sender（唯一出口：过滤链 + 审计）
```

三个容易踩的点：

- **所有消息都入队**，不只是被 @ 的。群聊里没被 @ 的内容是理解上下文的环境消息，按 token 预算压缩
  而不是丢弃。
- **传输读循环里不做任何 API 调用**。OneBot 的响应只能由同一个读循环读回，在那里 `Call` 必然死锁；
  引用解析因此放在 worker 里。
- **回复策略是路由规则**，不是 handler 里的分支——路由层就能回答「什么时候回复」。

### 5.3 提示词与缓存

提示词切成三段，顺序固定：

```
[静态段]   基础提示词 + 记忆指令 + 身份 + 工具提示   ← 模板引擎渲染，进程启动期内逐字节稳定
[半静态段] 人格设定 + 权限表（按角色）               ← 只随人格/角色/权限表变化
[动态段]   记忆块 + 只追加历史 + 当前输入            ← 每轮变化
```

- **唯一装配点**：`internal/conversation.Assembler`。agent 通过 `MessageAssembler` 接口拿到装配器，
  绝不自己拼消息。曾经装配器只有测试在调用、生产各自拼装，后果是配置里写了并记进日志的
  `ambient_token_budget` 和呈现窗口从未生效——现在有回归测试断言「实际发出的请求逐条等于装配器输出」。
- **记忆的位置**：记忆是一条独立消息，放在 system 之后、历史之前。这样既保住 system 段的全局缓存，
  又让记忆本身可以被缓存（见 [ADR-0002](docs/adr/0002-memory-injection-position.md)）。
- **高水位裁剪**：窗口成批滑动，两次移动之间完全稳定；逐轮滑动会让前缀每轮都变。
- **可回归**：`/prompt-hash` 报告各段哈希；`prompt_snapshots` 表记录每轮指纹并区分「记忆变更（预期）」
  与「前缀意外分歧（告警）」；`--stats` 给出命中率。

### 5.4 持久化

单个内嵌 SQLite，不开数据库服务端；表按**生命周期**分层：

| 层 | 内容 | 典型用途 |
|---|---|---|
| 消息流 | 归档消息与索引 | 全文检索、`recall_history`、导出 |
| 会话台账 | 会话元数据与用量归集 | `/cost`、`--stats`、会话回收 |
| 长期记忆 | 扁平记忆 + 分层（Working/Episodic/Semantic） | 记忆召回、遗忘、导出 |
| 在途状态 | 交互式等待、待审批 | 重启后恢复 / 通知 |
| 提示词快照 | 每轮提示词指纹与分段 | 缓存命中回归、前缀分歧告警 |

实现要点：单写者串行化、WAL、`BEGIN IMMEDIATE`、忙等默认 1s；schema 只在 `internal/store/schema.go`
声明并走版本化迁移；时间统一存 Unix 毫秒。JSONL 退化成导入/导出格式。

### 5.5 目录结构

```
AgentBot/
├── cmd/server/            组合根：main / serve / build / 各接线文件 / 维护命令
├── internal/              全部库代码
├── prompts/               人格与模板资产（私有内容不入库，见 prompts/README.md）
├── docs/adr/              架构决策记录
├── .scratch/agentbot/     规格与本轮 ticket（spec.md + issues/）
├── FEATURES.md            唯一功能规范（89 条）
├── HANDOFF.md             接手文档：现状、已完成的接线、已知偏离、工作方式
├── CHANGELOG.md           行为与对外契约的变化
├── DEPENDENCIES.md        依赖准入清单（含许可证核对）
└── config.example.yaml    带注释的示例配置
```

`internal/` 按职责分几层，常用包：

| 层 | 包 |
|---|---|
| 入口与编排 | `bot`、`session`、`router`、`reply`、`backpressure` |
| 平台接入 | `transport`、`event`、`outbound`、`httpx`、`admin` |
| 模型与提示词 | `llm`、`prompt`、`conversation`、`agent`、`tool`、`tool/builtin` |
| 记忆与检索 | `memory`、`history`、`vector`、`textsim`、`reflect`、`semcache`、`imagehash` |
| 安全与权限 | `access`、`policy`、`moderation`、`textguard`；工具沙箱策略在 `tool` 注册表里 |
| 成本与观测 | `cost`、`metrics`、`observe`、`trace`、`audit`、`ops` |
| 基础设施 | `config`、`store`、`secrets`、`reload`、`retry`、`scope`、`scoped`、`toggle`、`testutil`、`unsafeutil` |

### 5.6 关键设计决策

| 主题 | 决策 | 记录 |
|---|---|---|
| 工具调用协议 | 原生 `tool_calls` 是唯一执行通道；文本里的动作解析只是「抢救通道」 | [ADR-0001](docs/adr/0001-tool-call-protocol.md) |
| 记忆注入位置 | 记忆作为独立消息放在 system 之后、历史之前 | [ADR-0002](docs/adr/0002-memory-injection-position.md) |
| 持久化分层 | 单一内嵌 SQLite，表按生命周期分层，JSONL 退化为导入导出格式 | [ADR-0003](docs/adr/0003-persistence-and-data-layering.md) |
| 结构化输出（F-31） | 能力保留但暂不接线；先加 `memory_judge_verdicts_total` 观测"判官解析失败率"再决定 | [ADR-0004](docs/adr/0004-structured-output-not-wired.md) |

---

## 6. 开发

### 6.1 常用命令

```sh
go build ./...                    # 构建
go vet ./...                      # 静态检查
go test ./...                     # 跑测试
go test -race ./...               # 竞态检测（CI 使用；本地需要 cgo）
gofmt -l cmd internal             # 格式检查（应无输出）
go test -run=XXX -bench="." -benchtime=1x ./internal/... ./cmd/...   # 基准冒烟
```

> Windows 上基准必须写成 `-bench="."`：PowerShell 会把 `-bench=.` 里的点当成包参数，报出看起来
> 像「仓库坏了」的错误。

**本地跑不了 golangci-lint 和 `-race`**（需要 cgo/gcc），两者由 CI 把关——所以「本地全绿」不等于
能过 CI。

### 6.2 测试约定

- **Feature 编号进测试名**：`Test_F30_Backoff` —— 看到一个测试就知道它覆盖哪条 Feature。
- **fake 优先**：核心链路单测不依赖网络、不 sleep；`internal/testutil` 提供手写 Fake。
- **接线必须证明效果**：测试要证明「行为发生了变化」，而不是「函数被调用了」。
- **契约测试**：流式契约、单动作端到端、多轮工具调用。
- **Golden 测试**：模板 / 权限表渲染逐字节比对；CI 中禁止 `-update` 重写。

### 6.3 CI

[.github/workflows/ci.yml](.github/workflows/ci.yml) 有四类 job：

1. **build-test**：断言 `go.mod` 与 CI 的 Go 版本一致 → build → vet → golangci-lint →
   `go test -race`；
2. **golden-cross-platform**：Linux + Windows 矩阵，并断言 CI 中拒绝 `-update`；
3. **bench-coverage**：基准冒烟 + 覆盖率（低于阈值失败）；
4. 覆盖率产物上传。

### 6.4 Lint 硬规则

golangci-lint v2 配置见 [.golangci.yml](.golangci.yml)，启用 errcheck / govet / staticcheck /
gocritic / contextcheck / gosec / revive / forbidigo 等。几条硬规则：

- 库代码禁止 `log.Fatal` / `os.Exit` / `panic` / `time.Sleep` / 裸 `http.Get`；
- `unsafe` 只在 `internal/unsafeutil`；
- 错误一律 `fmt.Errorf("...: %w", err)`。

---

## 7. 局限与已知偏离

这些是**刻意的取舍或已知的未完成项**，不要当成 bug，也不要当成已完成：

> **完整的"实现了但没接线"清单在 [HANDOFF.md](HANDOFF.md) 第 4 节第 17 条**，
> 按"它失效时会怎样"分成两类：A 类会静默失效（文档或规格让你以为它已生效，
> 例如入站 guard 链、出站重试、F-32 的真实 usage 优先），B 类是宿主可用的内核 API。
> 改这些模块之前请先看那张表。

| 项 | 现状 |
|---|---|
| 多账号路由、多供应商路由 | 规范里有条目，但在设计目标之外，未实现 |
| 图片去重（感知哈希） | 库完整且有测试，但消费方需要「图片 → 视觉模型 → 描述」这条链路，而当前 `llm.Message` 只有文本。属**边界受限** |
| 分布式追踪 | 只实现了 `traceparent` 传播、采样与上下文，**没有 OTLP 导出**（OpenTelemetry 不在依赖白名单） |
| 工具沙箱 | 是**策略层**（白名单 + 结构化拒绝），不是操作系统级隔离。需要强隔离请把进程本身放进容器 |
| 沙箱的四个白名单键 | `read_roots` / `write_roots` / `env_allowlist` / `forbidden_ops` **目前不生效**：它们依赖工具实现 `SandboxDeclarer` 声明资源需求，而现有内置工具没有一个会用到路径、环境变量或命令参数，`Policy.Check` 收到的对应字段永远是空。真正生效的是 `forbidden_tools`、`max_output_bytes`、`require_read_only`，以及联网闸门 `network_tools` / `allow_network`（由 `http_fetch` 的声明驱动）。配置了却不生效的键会在启动时告警 |
| 成本的 `downgrade` 动作 | **未强制**：按请求切模型需要协议层支持，当前会如实告警并继续用原模型。会话/用户维度的计量与拒绝已生效 |
| 权限表的位置 | 规格把它归在「静态段」，实际放在半静态段——放进静态段会让前缀按角色分裂。这是两条规格的冲突，已显式裁定 |
| `llm.max_iterations` | 空转字段，会被校验但没有消费方；工具轮数由 `agent.max_iterations` 控制 |
| **入站传输模式 `wsserver` / `http`** | **未实现**：全仓库只有 `WSClient`（正向 WS）一个 Driver。这两个值过去能通过校验、`--check-config` 返回 0，进程随后带着空 `url` 打印「已启动」并无限重连，与网络故障无法区分；现在会在启动前被**直接拒绝**。恢复它们时要把「入站必须配 `access_token`」一并加回来 |
| `/prompt-hash` 的静态段 | 只覆盖 system 正文；工具 schema 随请求发送，顺序由注册顺序固定，不计入静态段 |

---

## 8. 文档索引

| 文档 | 内容 |
|---|---|
| [FEATURES.md](FEATURES.md) | 唯一功能规范：89 条 Feature 的价值 / 规格 / 边界 / 验收 |
| [GLOSSARY.md](GLOSSARY.md) | 领域词汇表：会话、记忆、前缀缓存、路由、安全策略的规范用词 |
| [HANDOFF.md](HANDOFF.md) | 现状、已完成的接线与对应测试、已知偏离、每轮收尾流程 |
| [config.example.yaml](config.example.yaml) | 逐项注释的配置示例（配置的权威参考） |
| [CHANGELOG.md](CHANGELOG.md) | 行为与对外契约的变化（配置项、命令行、指标名、规范条目） |
| [docs/adr/](docs/adr/) | 架构决策记录：工具调用协议 / 记忆注入位置 / 持久化分层 / 结构化输出是否接线 |
| [prompts/README.md](prompts/README.md) | 提示词资产目录的用法与写提示词的经验 |
| [.scratch/agentbot/spec.md](.scratch/agentbot/spec.md) | 本轮目标、已定决策、ticket 索引与里程碑 |
| [DEPENDENCIES.md](DEPENDENCIES.md) | 依赖准入清单与许可证核对 |
| [AGENTS.md](AGENTS.md) | 协作者（人或 AI）的入口约定 |

---

## 许可

MIT。第三方依赖及其许可证见 [DEPENDENCIES.md](DEPENDENCIES.md)。
