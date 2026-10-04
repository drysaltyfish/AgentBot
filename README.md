# AgentBot

> 跑在 QQ（OneBot v11）上的自用 AI 机器人：**单二进制 + 同目录配置 + 内嵌 SQLite**，不依赖任何外部服务。
> 把群聊/私聊消息交给大模型，带着长期记忆、工具调用与人格设定回复；每条行为都能在日志、指标或台账里查到。

---

## 目录

- [1. 项目简介](#1-项目简介)
- [2. HOW TO USE](#2-how-to-use)
- [3. 它能做什么](#3-它能做什么)
- [4. 配置文件](#4-配置文件)
- [5. 架构](#5-架构)
- [6. 开发、测试与协作](#6-开发测试与协作)
- [7. 文档索引](#7-文档索引)

---

## 1. 项目简介

AgentBot 是一个**工程纪律优先**的实现：先立规矩（lint / CI / 手写 Fake / 唯一装配点 / 优雅关闭），再写业务。
它的设计取向刻意保守，理解下面这几条就读懂了大部分取舍：

| 取向 | 具体含义 |
|---|---|
| **单二进制 + 同目录配置** | 不做 K8s / Helm / Makefile，不引入数据库服务端。部署 = 拷一个可执行文件 + 一份 YAML + 一个数据目录。 |
| **缓存优先** | 提示词切成「不可变前缀 → 半静态段（人格/权限表）→ 记忆 → 只追加历史 → 当前输入」。前缀逐字节稳定才能命中 provider 的前缀缓存，命中率是**可查询、可回归**的指标。 |
| **行为必须可见** | 关键路径都有结构化日志；token、请求、缓存命中、成本落在台账里可查；失败与降级留线索而不是静默吞掉。 |
| **fail-closed** | 配置校验不过就启动失败；入站模式没配令牌就拒绝启动；权限表没配好就不放行；持久层打不开就报错退出，绝不悄悄退回内存。 |
| **不接线即未交付** | 库写完了但组合根没接上，按仓库纪律（F-79）一律记为**未完成**；启动日志会打印一份由**装配事实**推出的能力清单。 |
| **不依赖既有项目** | 全部自行组装；调研目录不参与构建、不进版本控制。 |

它**不是**：通用聊天框架、多租户服务、带 Web UI 的产品。

### 现状

规范是 [FEATURES.md](FEATURES.md)，共 **89 条**。

- **功能实现 87 / 89**：未实现的两条是 **F-07 多账号路由** 与 **F-27 多供应商路由**，均在设计目标之外。
- **接线全部完成**：包括配置热加载、分层记忆、混合检索、摘要树、语义缓存、流式发送、成本配额、追踪传播、权限即提示词、提示词模板、上下文预算等。
- **唯一保留项 F-62**（感知哈希图片去重）：库（pHash / 缓存 / 分桶 / LRU）完整且有测试，但它的消费方需要「图片 → 视觉模型 → 描述」这条链路，而 FEATURES 里没有任何一条定义该上游能力（当前 llm.Message 只有文本）。因此它被记为**边界受限**而不是「已交付」。
- 已知偏离（无 OTLP 导出、沙箱是策略层而非 OS 隔离、cost 的 downgrade 未强制等）集中在 [HANDOFF.md](HANDOFF.md) 的「已知偏离」一节，逐条说明原因。

---

## 2. HOW TO USE

### 2.1 前置条件

| 依赖 | 说明 |
|---|---|
| Go **1.27.1** | go.mod、CI、.golangci.yml 三处锁定同一版本 |
| OneBot v11 实现 | 例如 NapCat / Lagrange，暴露 WebSocket（本仓库默认按 wsclient 连出） |
| OpenAI 兼容模型端点 | 默认按 DeepSeek 配置；也支持 openai 与本地 echo 假模型 |

### 2.2 快速开始

```sh
git clone git@github.com:drysaltyfish/AgentBot.git
cd AgentBot

# 1) 准备配置
cp config.example.yaml config.yaml
#    至少改三处：llm.api_key（或 api_key_env / `${VAR}`）、transport.url、transport.self_id

# 2) 校验配置：打印**脱敏后**的生效配置，然后退出，不启动服务
go run ./cmd/server --config config.yaml --check-config

# 3) 启动
go run ./cmd/server --config config.yaml
```

### 2.3 命令行

所有子命令都是**一次性维护动作**：执行完打印结果并退出。

| 参数 | 说明 |
|---|---|
| --config <path> | 配置文件路径，默认 config.yaml |
| --check-config | 只校验配置并打印脱敏后的生效配置后退出。部署前确认解析结果用 |
| --selftest <QQ> | 连接平台 → get_login_info → 给该 QQ 发一条自检消息 → 退出。用于打通整条链路 |
| --stats | 打印用量台账：消息数、请求数、工具调用数、输入/输出/推理 token、前缀缓存命中率、估算成本、花费最高的会话 |
| --export-memories <path> | 把**全部**记忆导出为 JSONL 后退出（跨作用域，因此属于维护命令而非会话内能力） |

### 2.4 构建与部署

```sh
go build -o bin/agentbot ./cmd/server
```

最小部署清单：

```
agentbot                # 单一可执行文件
config.yaml             # 唯一配置
data/                   # 运行时数据目录（首次启动自动创建）
```

systemd 单元示例（要点：设置工作目录，让相对路径与 config.yaml 落在同一处）：

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

启动流程是**先校验、后监听**：配置非法、密钥缺失、持久层打不开、人格或权限表非法，都会以非零退出码失败，不会带病运行。

### 2.5 运行时数据文件

| 路径（默认） | 内容 | 说明 |
|---|---|---|
| data/agentbot.db | 唯一 SQLite（WAL） | 消息归档、会话台账、长期记忆、在途操作、提示词快照 |
| data/agentbot.db-wal / -shm | WAL 附属文件 | SQLite 正常产物 |
| data/history.jsonl | 旧版历史（可选） | **一次性导入**源；导入幂等，可留可删 |
| data/memory.jsonl | 旧版记忆（可选） | **一次性导入**源；导入进 SQLite 的扁平记忆表 |
| data/audit.jsonl | 审计日志 | 只追加；不可关闭 |
| data/toggles.json | 功能开关状态 | 让 /switch 的按群开关重启后保持 |
| data/blacklist.json | 黑名单与临时封禁 | 让 /ban 与防刷结果重启后保持 |

### 2.6 运维入口

启用 ops 后（默认启用，只绑回环）有三个端点：

| 端点 | 用途 |
|---|---|
| GET /metrics | Prometheus 文本格式指标：请求、延迟、token、缓存命中、成本、限流拦截、审查拦截等 |
| GET /healthz | 进程存活（不查依赖） |
| GET /readyz | 依赖就绪：存储可用等，结果按 ready_cache_ttl 缓存 |

另外两条排障路径：

- **结构化日志**：JSON 行，带 trace_id；命中出站请求时会发出 traceparent 头，可把日志与平台侧链路对上。
- **启动能力清单**：启动日志里 capabilities 字段是本次装配**真实启用**的能力（不是固定清单）。

### 2.7 常见启动失败

| 现象 | 原因 |
|---|---|
| transport.url 必须提供 | mode=wsclient 未填上报地址 |
| 入站模式必须配置 access_token | mode=wsserver/http 是入站入口，fail-closed（F-80） |
| llm.model 必填 | 未配置模型名 |
| 环境变量 XXX 未设置 | 某个 `${VAR}` 引用指向不存在的环境变量（这是刻意的：静默变空串比启动失败更难查） |
| 权限表 / 人格 / 敏感词正则非法 | fail-fast：安全与提示词资产配错不允许带病启动 |
| 打开数据库失败 | store.path 不可写或磁盘损坏；不会降级为内存 |
| 名单模式未写 / allow 模式名单为空 | access 的 fail-fast：防止"以为开了其实没开"或"把所有人挡在门外" |

### 2.8 常用配置：超管与名单（按 QQ 号）

只处理指定的群与人（其余消息在路由层直接丢弃）：

@@@yaml
access:
  enabled: true
  mode: allow                 # 白名单；deny 则是黑名单
  users: [10001, 10002]       # 允许的 QQ 号
  groups: [123456789]         # 允许的群号
  check_users_in_group: true  # 群里也要求人在名单里
  roles:
    superuser: [10001]        # 超管（可用全部管理命令、永不封禁、绕过名单）
    admin: [10002]            # policy 角色，可用 /switch
@@@

超管也可以继续写在原来的位置（两者取并集）：

@@@yaml
moderation:
  enabled: true               # 需要 /ban 等命令时开启
  super_users: [10001]
@@@

细则（角色能力对照、名单语义、审计与指标）见第 3.7 节与第 4.2 节。

---

## 3. 它能做什么

按**用户可感知**的行为组织。「可配置项」的完整解释见第 4 节。

### 3.1 聊天与回复

- **私聊 / 群聊两套策略**：私聊可设为 always / never；群聊可设为 always / on_mention（默认）/ never。群聊默认只在被 @ 时回复，避免刷屏。
- **环境消息参与上下文**：群里没被 @ 的消息不会回复，但会作为背景进入模型上下文，并按单独的 token 预算压缩——刷屏不会把真正的对话挤出窗口。
- **分段发送**：回复里的空行会被拆成多条消息（真人是一条一条发的），带连发间隔与最大条数上限，超出部分合并进最后一条。
- **流式增量发送**（可选，默认关闭）：直连模型路径下，边生成边发送；增量同样经过统一出口与过滤链。ReAct 路径会自动退回整段发送（它每轮都在等完整工具结果）。
- **单飞（反并发）**（可选）：同一用户（或用户+群）连点两次时，第二次在入口被拒，避免两次昂贵调用；可选回一句提示。

### 3.2 工具调用（10 个内置工具）

| 工具 | 作用 |
|---|---|
| calculator | 四则运算与常见数学表达式 |
| current_time | 当前时间（可按配置时区） |
| json_query | 在 JSON 文本里按路径取值 |
| http_fetch | 抓取 URL（走安全 HTTP 客户端：白名单、体积上限、超时） |
| get_user_info | 查询群成员资料（需要平台 API） |
| memory_save | 保存一条长期记忆 |
| memory_recall | 召回当前作用域的记忆 |
| recall_history | 按关键词回溯当前会话历史（含命中处前后文；可换混合检索/摘要树） |
| forget_memory | 遗忘指定记忆 |
| list_memories | 列出当前作用域的记忆 |

工具相关能力：

- **按需注册**：agent.tools 留空 = 全部注册；列出名单 = 只注册这些（顺序恒定，保护前缀缓存）。
- **权限与人工审批**（可选，默认关闭）：工具 × 角色 → 放行 / 需审批 / 拒绝；需要审批但没有审批通道时按拒绝处理（fail-closed）。
- **虚拟动作**：end_action / save_memory / noop 三个闭环动作。
- **沙箱**（可选，默认关闭）：限制工具的读写路径、可透传环境变量、联网能力与输出体积，越权返回结构化拒绝。

### 3.3 记忆

- **分层记忆**：Working（当前工作集）/ Episodic（情节）/ Semantic（语义事实）三层；Semantic 复用扁平记忆表，因此遗忘/导出/去重语义一致。
- **两条写入通道**（可同时开启）：
  - **规则触发**：用户说「记住：xxx」时无条件写入——确定、不花模型调用、可审计；
  - **模型主动**：在系统提示词末尾追加指令，让模型遇到值得长期记住的事实时调用 save_memory——不依赖用户明说。
- **语义判官**：字符相似度做不了语义判断（实测「旧的一条」与「新的一条」相似度正好 0.50 却是两件不同的事）。写入因此是混合判据：完全相同 / 相似度 ≥ 0.90 / < 0.30 走确定性判定；落在歧义带才用**关闭思考**的模型问一次，结果按文本对缓存；判官不可用时退回确定性判据，写入照常成功。
- **作用域隔离**：记忆严格按会话作用域隔离，跨用户的记忆不会串台。
- **遗忘 / 导出 / 留存**：记忆可遗忘单条、检视列表；--export-memories 全量导出 JSONL。

### 3.4 人格

- **目录式定义**：每个 <name>.yml 一个人格，name 必须与文件名一致；启动期校验引用，写错人格名在启动时就会失败。
- **会话级切换**：/persona 切换当前会话的人格，切换结果落 SQLite，重启后保持。
- **与提示词前缀的关系**：人格只改变**半静态段**（静态前缀之后的字节），因此不同人格的会话仍共享同一段静态前缀缓存。

### 3.5 历史与召回

- **呈现窗口与存储保留分离**：存储保留量（默认 400 条）远大于回灌窗口（默认 20 条），这样 recall_history 才有窗口之外的内容可召回。
- **高水位批量裁剪**：窗口成批滑动而不是逐轮滑动——逐轮滑动会让请求前缀每轮都变，缓存必然失效。
- **混合检索**（可选，默认关闭）：recall_history 改用「BM25 关键词 + 二值向量 + RRF 融合」，专有名词与改写表达都能命中。
- **摘要树**（可选，默认关闭）：独立于关键词/向量的第三条召回源，在摘要层检索，宏观问题可以命中上层摘要。

### 3.6 管理命令（聊天内）

| 命令 | 作用 | 权限 |
|---|---|---|
| /help | 列出全部可用管理命令 | 超管（access.roles.superuser） |
| /switch <plugin> on\|off | 按群开关某个插件，状态落盘、重启保持 | 群 owner / admin（需 toggle.enabled） |
| /ban <用户号> [原因] | 永久封禁 | 超管（需 moderation.enabled） |
| /unban <用户号> | 解除封禁 | 超管（需 moderation.enabled） |
| /banlist | 列出当前封禁 | 超管（需 moderation.enabled） |
| /cost | 今日 / 累计 / 本会话的调用次数与费用 | 超管（需 cost.enabled） |
| /persona <名称> | 切换当前会话人格 | 超管 |
| /prompt-hash | 打印提示词各段哈希（静态 / 半静态 / 动态） | 超管 |

命令的每一次调用（无论通过与否）都会进审计。

### 3.7 谁可以用它：名单与角色（按 QQ 号）

- **名单**（access，默认关闭）：两种模式——allow（白名单，只有名单内处理）、deny（黑名单，名单内丢弃）。
  命中即在**路由层直接丢弃**：不匹配路由、不建会话、不落库、不产生模型调用。
  名单按 **QQ 号**与**群号**配置；用户名单可以用 check_users_in_group 控制"是否在群里也生效"。
- **看得到内容 vs 看不到内容**：名单只看得到 QQ 号/群号（最省事、最彻底）；
  如果规则需要看消息内容（敏感词等），那是 moderation 的职责，两者可以同时开。
- **超管绕过名单**：默认开启，避免"白名单配错把自己锁在门外"——那种情况只能改文件重启。
- **用 QQ 号指定角色**（access.roles）：superuser / owner / admin / member，
  优先于平台上报的群成员角色。同一个 QQ 写在多个角色里时按权限取高。
- **审计与指标**：每次丢弃都进审计（inbound_blocked）与 events_dropped 指标；
  log_drops 打开时再额外逐条记日志。

#### 怎么设置超管（按 QQ 号）

超级管理员只有一个位置：**access.roles.superuser**（moderation.super_users 已移除），
拥有：聊天内管理命令授权、policy 的 superuser 角色、永不封禁、绕过名单。

@@@yaml
# 超管的**唯一**位置（moderation.super_users 已移除，出现旧键会启动失败）：
access:
  roles:
    superuser: [10001]      # 超管
    owner:     [10002]      # policy 的最强角色；也能用 /switch
    admin:     [10003]      # policy 角色；能用 /switch
    member:    [10004]      # 普通成员（一般不用显式配）
@@@

角色能力对照：

| 角色 | 管理命令 | policy 权限表 | 其他 |
|---|---|---|---|
| superuser | 全部（/help /ban /unban /banlist /cost /persona /prompt-hash） | 内置表里权限最高 | 永不封禁、绕过名单 |
| owner | /switch | 内置表里次高 | 平台上报的群主也是这一档 |
| admin | /switch | 内置表：不能禁言 | 平台上报的群管理员也是这一档 |
| member | — | 内置表：只有 send_msg | 默认档 |

> 平台上报的 owner/admin 无需配置；access.roles 用来**覆盖**它们（例如把某个 QQ 直接指定为超管）。

### 3.8 群管理与防刷

- **入站内容审查**（可选）：敏感词命中后按 mask（脱敏放行）或 block（拦截）处理；词表支持内联 + 文件（每行一个词，支持 # 注释），并且**热加载**。
- **黑名单与防刷**（可选）：封禁用户/群；窗口内超阈值或连续重复消息触发临时封禁；超管永不封禁。
- **限速**（可选，默认关闭）：令牌桶按用户与群限额，超限事件被整条丢弃（连「只记录」的兜底路由也不执行）；参数**热加载**。
- **功能开关**：/switch 按群隔离，状态落盘。

### 3.9 成本与缓存

- **成本统计与配额**（可选）：按真实 usage 记账，支持 global / session / user 三个维度与 day / month / total 周期；软限告警、硬限在调用前拒绝（拒绝发生在花钱之前）。
- **语义缓存**（可选，默认关闭）：常见问题命中缓存可直接作答，零 token；命中条件是「问题相似度达标 + 上下文指纹（人格/系统提示词）一致」；带工具调用的轮次不缓存。
- **前缀缓存友好**：提示词前缀逐字节稳定，命中率可在 --stats 与指标里查；提示词快照记录每轮指纹，并区分「记忆变更（预期）」与「前缀意外分歧（告警）」。

### 3.10 可观测性

- **结构化日志**：JSON 行 + 组件标签 + trace_id；debug_content 打开时可记录内容（默认只记长度与摘要）。
- **指标**：上述所有开关性的能力都有对应计数/耗时指标，/metrics 直接抓。
- **审计**：管理员命令、封禁、审查拦截、审批等待、出站消息等安全相关事件，落地 JSONL 并可同时写 stdout。**审计不可关闭**。
- **分布式追踪**：入站事件建立 W3C trace 上下文，日志与出站 traceparent 一致；采样默认 1%（传播不受采样影响）。

### 3.11 稳定性与安全

- **优雅关闭**：收到信号后按预算关闭组件（默认 10s），期间不再接新事件，在途等待会被持久化。
- **在途恢复**：交互式等待与审批落 SQLite，重启后至少能通知原会话，而不是让用户一直干等。
- **密钥管理**：支持 api_key_env（环境变量）> api_key_file（密钥文件）> api_key（内联，启动告警）；`${VAR}` 引用未设置即启动失败。
- **传输鉴权**：入站模式必须配 access_token；支持签名校验与 IP 白名单。
- **出站 HTTP 安全**：统一客户端（白名单、超时、体积上限、重定向策略），不允许裸调用。
- **唯一出口**：所有出站消息经过过滤链与审计，敏感内容在一处处理。
- **权限双层**：提示词里按角色渲染权限表（输入侧约束），平台 API 出口按角色硬拦截（执行侧，fail-closed）。

---

## 4. 配置文件

一份 YAML，全部字段见 [config.example.yaml](config.example.yaml)（带注释）与 internal/config/sections.go（唯一 schema）。
本节按**特性**组织，逐项解释每个可配置项。默认值一栏写「未配置时」的解析结果。

### 4.1 通用约定

| 约定 | 含义 |
|---|---|
| 未设置 vs 零值 | 可选字段一律用指针表达；0 / 空串是明确的值，不是「未设置」 |
| 时长 | 写 Go 风格字符串：10s、1500ms；负数直接报错 |
| 密钥引用 | `${VAR}` 可出现在 transport.access_token、transport.signature_secret、llm.api_key；变量未设置即启动失败 |
| 校验时机 | 启动时全部校验并一次性报告全部问题（F-25）；CI 有测试保证示例配置的每个键都在 schema 里存在 |
| 脱敏 | --check-config 输出的密钥一律替换为掩码 |

### 4.2 access：名单与角色（按 QQ 号 / 群号）

默认关闭。开启后，名单外的消息在**路由层直接丢弃**（不匹配路由、不建会话、不落库、不调用模型）。

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 是否启用名单 |
| mode | string | 无 | allow（白名单）/ deny（黑名单）；启用时必须显式写，否则启动失败 |
| users | []int64 | 空 | 名单里的 **QQ 号** |
| groups | []int64 | 空 | 名单里的**群号** |
| check_users_in_group | bool | true | 用户名单是否也在群里生效：allow 模式下为 true 时要求"群在名单 + 人在名单"；deny 模式下为 true 时群内这些人也会被丢弃 |
| bypass_super_users | bool | true | 超管是否绕过名单。默认绕过以避免"配错白名单把自己锁在门外"；关掉它且没有任何超管时启动失败 |
| log_drops | bool | false | 是否逐条记录被丢弃的消息；无论开关，丢弃都会进审计与 events_dropped 指标 |
| roles | map | 空 | 用 QQ 号直接指定角色，**独立于 enabled**：即使名单关闭，角色指定照样生效 |

roles 支持的角色名（写错会让启动失败）：

| 角色 | 效果 |
|---|---|
| superuser | 管理命令授权、policy 的 superuser 角色、永不封禁、绕过名单 |
| owner | policy 的 owner 角色；可用 /switch |
| admin | policy 的 admin 角色；可用 /switch |
| member | policy 的 member 角色；可用来**降级**平台上报的 owner/admin |

说明：

- **超管只有一个位置**：access.roles.superuser。管理命令授权、权限判定、名单绕过、never-ban 读的都是这一份。旧键 moderation.super_users **已移除**：出现即启动失败并给出迁移指引（不静默忽略，免得表现成"我明明是超管，命令却不管用"）。
- **角色优先级**：access.roles 显式指定 > 超管名单 > 平台上报的群成员角色 > everyone。
- **冲突按权限取高**：同一个 QQ 被写进多个角色时取权限更高的那个，不让配置书写顺序决定权限。
- **只会看到 QQ 号与群号**：需要按消息内容拦截（敏感词等）请用 moderation，两者可同时开启。

### 4.3 transport：平台连接

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| mode | string | 无（必填） | wsclient（连出，默认场景）/ wsserver / http |
| url | string | 无 | mode=wsclient 时必填的上报地址，例如 ws://127.0.0.1:3001 |
| access_token | string | 无 | 平台访问令牌；**入站模式（wsserver/http）必填**，否则拒绝启动 |
| signature_secret | string | 无 | 入站签名密钥；配置后校验请求签名 |
| ip_allowlist | []string | 空 | 入站来源 IP 白名单；为空表示不限制 |
| self_id | int64 | 0 | 机器人自身 QQ 号。用于识别「@ 自己」、会话键计算、自检目标 |
| backoff | duration | 1s | 断线重连的退避基准 |

### 4.3 llm：模型接入

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| provider | string | openai | echo（假模型，联调用）/ openai / deepseek |
| model | string | 无（必填） | 模型名 |
| base_url | string | 按 provider | 自定义端点；留空用 provider 默认 |
| api_key | string | 无 | 内联密钥（不推荐；启动会告警）。支持 `${VAR}` |
| api_key_env | string | 无 | 优先读取的环境变量名（第一优先级） |
| api_key_file | string | 无 | 密钥文件（第二优先级）；Unix 下建议 0600，权限过宽只告警 |
| timeout | duration | 30s | 单次模型请求超时 |
| max_iterations | int | 10 | **当前未生效**：字段会被校验，但没有任何消费方；工具调用轮数由 agent.max_iterations 控制（见 HANDOFF 已知偏离） |
| thinking | bool | 不下发 | 是否开启思考模式（DeepSeek）；未设置 = 不下发该字段 |
| reasoning_effort | string | 无 | low / high / max |
| system_prompt | string | 内置默认 | 不可变前缀正文（缓存优先的关键） |
| system_prompt_file | string | 无 | 从文件读前缀正文，**优先级高于 system_prompt**；启动时读一次并固定 |
| history_turns | int | 20 | 最多回灌多少条历史（呈现窗口） |
| ambient_token_budget | int | 1200 | 环境消息（群里没被 @ 的）token 预算；**负数 = 不压缩** |
| ambient_max_chars | int | 200 | 单条环境消息字符上限；超出截断并提示模型可 recall_history 回溯 |
| max_context | int | 0（关闭） | F-32 上下文预算：模型窗口 token 数；>0 才启用裁剪 |
| reserve_output | int | 1024 | 预留给输出的 token |
| reserve_tools | int | 512 | 预留给工具 schema 的 token（实际占用更大时按实际预留） |
| pricing.version | string | 空 | 价格版本号，写进台账，便于解释历史数据 |
| pricing.input_per_million | float | 0 | 输入价（每百万 token，美元） |
| pricing.output_per_million | float | 0 | 输出价 |
| pricing.cache_hit_per_million | float | 0 | 缓存命中价 |

说明：

- **预算裁剪**（F-32）打开后，每次请求按「窗口 − 输出预留 − 工具预留」裁剪；system 消息被标记为 pinned，任何裁剪都不会丢掉系统提示词。
- **定价全 0 = 不统计成本**，但 token 与请求数照记——「花了多少 token」与「花了多少钱」是两件事。
- system_prompt(_file) 在启动时固定，**刻意不热加载**：运行时读文件会让前缀在运行中变化、缓存全废。

### 4.4 store：持久层

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| path | string | data/agentbot.db | SQLite 文件路径；目录会自动创建 |
| busy_timeout | duration | 1s | 忙等超时。刻意取短值：长超时会让并发写者按同一节奏退避 |

开启 WAL、单写者串行化（BEGIN IMMEDIATE）、抖动重试；schema 统一在 internal/store/schema.go 声明并走版本化迁移。**打不开即启动失败**，不会退回内存。

### 4.5 history：对话历史

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| file | string | 空 | 旧版 JSONL 历史路径；为空则不做旧数据导入（新数据始终落 SQLite） |
| retention | int | 400 | **存储**保留条数上限，远大于呈现窗口；两者分开，recall_history 才召回得到窗口外的内容 |

### 4.6 agent：Agent 循环、工具与记忆

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | true = 回复走 ReAct 循环（可调用工具）；false = 直连模型 |
| paradigm | string | 空/react | react（基础）/ reflexion / orchestrator |
| reflexion.max_reflections | int | 1 | 反思最大轮数 |
| reflexion.threshold | float | 1.0 | 「够好就停」的分数阈值 |
| max_iterations | int | 10 | ReAct 最大迭代轮数 |
| step_timeout | duration | 30s | 单个工具的执行超时；人工审批等待占**独立预算**，不计入这里 |
| protocol | string | auto | native = 只消费 provider 原生 tool_calls；auto = 原生为主，文本里的动作由解析器抢救 |
| virtual_actions | bool | 未配置按下发 | 注册 end_action / save_memory / noop |
| tools | []string | 空 = 全部 | 只注册列出的内置工具（顺序恒定） |
| memory | bool | 未配置按启用 | 启用长期记忆（落 SQLite） |
| memory_max | int | 64 | 每个作用域的条数上限 |
| memory_file | string | 空 | **旧版记忆 JSONL**：仅作一次性导入源（导入进扁平记忆表，幂等） |
| approval_enabled | bool | false | 启用 F-45 权限闸门。空权限表是 fail-closed 的，没配审批通道就打开会让工具全部不可用 |
| allow | map | 空 | 「工具 → 角色」放行表：{calculator: [member, private]}。角色：owner / admin / member / private |
| approval_timeout | duration | 60s | 人工审批等待预算 |
| auto_memory.enabled | bool | false | 规则触发写入（「记住：xxx」） |
| auto_memory.triggers | []string | 内置触发词 | 覆盖默认触发词 |
| proactive_memory.enabled | bool | 未配置按启用 | 让模型自己判断什么值得长期记住（概率性） |
| proactive_memory.instruction | string | 内置指令 | 覆盖追加到系统提示词末尾的指令 |
| memory_judge.enabled | bool | 未配置按启用 | 语义判官：只在相似度落歧义带时问一次模型 |
| tool_hint.enabled | bool | 未配置按启用 | 追加工具使用提示（例如提示可用 recall_history 回溯） |
| tool_hint.instruction | string | 内置提示 | 覆盖提示正文 |

### 4.7 behavior：回复行为

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| private | string | always | 私聊策略：always / never |
| group | string | on_mention | 群聊策略：always / on_mention / never |
| split_on_blank_line | bool | true | 回复里的空行拆成多条发送（段内单个换行不拆） |
| split_delay | duration | 400ms | 连发之间的间隔 |
| max_segments | int | 4 | 单次回复最多拆几条，超出部分合并进最后一条 |

### 4.8 prompt：提示词资产

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| dir | string | prompts | 模板与人格资产的根目录 |
| persona | string | 无 | 启动时的默认人格名 |
| personas_dir | string | dir/personas | 人格定义目录；为空时取 prompt.dir 下的 personas 子目录 |

模板引擎会把静态前缀（基础提示词 + 记忆指令 + 身份 + 工具提示）按 dir 下的同名模板（如 system.tmpl）渲染；缺失则回退内置版本。模板在**启动期**校验（语法错误、变量名写错都会让启动失败），行尾统一为 LF。渲染结果只随配置变化，**不含时间**——静态前缀是所有会话共享的缓存前缀。

### 4.9 policy：权限表

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| file | string | actions.yaml | 权限表文件。**覆盖**语义：文件不存在用内置默认表并记日志；存在但解析/校验失败则启动失败；存在时热加载 |

权限表用于两处：渲染进提示词的半静态段（按角色），以及在平台 API 出口硬拦截（角色不允许的 action 直接拒绝）。角色映射：超管名单（access.roles.superuser）优先，其次平台上报的 owner / admin / member，其余按 everyone（fail-closed）。

### 4.10 log：结构化日志

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| level | string | info | debug / info / warn / error |
| format | string | json | json 或 text |
| components | map | 空 | 按组件覆写级别，例如 {llm: debug} |
| debug_content | bool | false | 是否记录消息正文；默认只记长度/摘要，避免日志成为内容泄漏渠道 |
| queue_size | int | 1024 | 异步写队列容量；写不进去只丢日志并计数，不影响主流程 |

### 4.11 shutdown：优雅关闭

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| timeout | duration | 10s | 关闭预算：超时后强制结束，避免卡死在半关闭状态 |

### 4.12 ratelimit：令牌桶限速

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：超限事件会被整条丢弃（改变行为，需显式开启） |
| user_per_minute | int | 20 | 单用户每分钟次数 |
| user_burst | int | 5 | 单用户突发容量（桶容量） |
| group_per_minute | int | 120 | 单群每分钟次数 |
| group_burst | int | 20 | 单群突发容量 |

键是用户与群；空闲键按 burst/rate×3 的 TTL 自动回收。**这几个参数热加载**：改配置文件后按新参数重建桶，从下一次请求生效。

### 4.13 toggle：功能开关

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 是否启用 /switch 按群开关 |
| default_on | bool | true | 未显式设置过的 (plugin, group) 的默认状态 |
| file | string | 空 | 开关状态落盘路径；为空仅进程内（重启即丢） |

### 4.14 audit：审计日志

审计**不可关闭**（规格要求），只能调去处与内容保留量。

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| file | string | 空 | 审计 JSONL 路径；为空只写 stdout |
| stdout | bool | false | 是否同时写标准输出（便于采集器抓） |
| queue_size | int | 4096 | 异步队列容量；满或写失败只丢审计并计数 |
| content_limit | int | 20 | 用户内容只保留前 N 个字符，其余以 …(len=N) 代替 |

### 4.15 ops：指标与探针

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | true | 是否启用独立监听 |
| addr | string | 127.0.0.1:9090 | 监听地址；默认只绑回环，这也是默认不配鉴权也安全的前提 |
| auth_token | string | 空 | 非空时三个端点都要求 Authorization: Bearer <token> |
| ready_cache_ttl | duration | 10s | /readyz 结果缓存时长 |
| probe_timeout | duration | 1s | 单次依赖检查超时 |

### 4.16 singleflight：单飞（反并发）

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：会让同一 key 的第二次并发请求被拒，属改变行为的开关 |
| key | string | user_group | user（按用户）或 user_group（用户+群） |
| notice | bool | false | 被拒绝时是否回一句「正在处理中」 |

### 4.17 moderation：入站审查、黑名单与防刷

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：会改写或拦截入站消息 |
| sensitive_words | []string | 空 | 内联敏感词，与文件叠加 |
| sensitive_words_file | string | 空 | 词表文件（每行一个词，忽略空行与 # 注释）；**热加载**，编译失败保留旧表 |
| action | string | mask | mask（脱敏放行）或 block（拦截） |
| mask_replacement | string | 按命中长度生成等长掩码 | 脱敏替换串 |
| blacklist_file | string | 空 | 封禁落盘路径；为空仅进程内（重启即丢） |
| super_users | []int64 | — | **已移除**：超管请改用 access.roles.superuser（见 4.2）。旧键出现会让启动失败并给出迁移指引 |
| antispam.window | duration | 10s | 速率统计窗口 |
| antispam.max_messages | int | 20 | 窗口内允许的最大消息数 |
| antispam.ban_duration | duration | 60s | 触发后的临时封禁时长 |
| antispam.duplicate_repeat | int | 5 | 连续相同消息的阈值 |

### 4.18 sandbox：工具执行沙箱

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：会限制既有工具的能力 |
| max_output_bytes | int | 65536 | 单次工具输出上限 |
| read_roots / write_roots | []string | 空 | 允许读/写的路径白名单根 |
| env_allowlist | []string | 空 | 允许透传给工具的环境变量名 |
| forbidden_ops | []string | 空 | 禁止的操作名 |
| forbidden_tools | []string | 空 | 禁止的工具名 |
| network_tools | []string | 空 | 允许联网的工具（其余默认禁网） |
| allow_network | bool | false | 是否默认放开联网 |
| require_read_only | bool | false | 是否要求工具只读 |

注意：本仓库的沙箱是**策略层**实现（白名单 + 结构化拒绝），不是操作系统级隔离（无容器/命名空间）；需要强隔离请把进程本身放进容器。

### 4.19 cost：成本统计与配额

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭 |
| unknown_model | string | warn_zero | 未识别模型：warn_zero（0 计费并告警）或 reject |
| queue_size | int | 256 | 异步持久化队列长度 |
| prices | map | 空 | 每模型价格：prices.<model>.input_per_1k / output_per_1k（每千 token） |
| quotas | [] | 空 | 配额列表，字段见下 |

单条配额字段：

| 字段 | 取值 | 说明 |
|---|---|---|
| scope | global / session / user | 配额维度 |
| period | day / month / total | 统计周期 |
| limit | float | 硬限金额 |
| soft_limit | float | 软限金额（只告警） |
| action | deny / downgrade / warn | 触限动作；**downgrade 目前未强制**（按请求切模型需要协议层支持），会告警并继续用原模型 |
| downgrade_model | string | downgrade 动作建议使用的模型名（仅用于告警文案） |

### 4.20 semcache：语义缓存

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：它改变「同一句话在不同时刻得到什么回答」，省下的只是重复问题的 token |
| threshold | float | 0.95 | 命中所需最低相似度，取值 (0,1] |
| ttl_seconds | int | 3600 | 条目生存时间 |
| max_entries | int | 4096 | 容量上限（超出按 LRU 淘汰） |
| skip_words | []string | 空 | 在内置跳过列表（时间/天气等易变话题）之外追加的关键词 |
| skip_patterns | []string | 空 | 附加正则跳过规则；**语法错误会让启动失败** |

命中条件 = 问题相似度达标 **且** 上下文指纹（人格 / 系统提示词）一致；缓存前会过一遍出口过滤链；带工具调用的轮次不进缓存。

### 4.21 stream：流式增量发送

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭 |
| max_chars | int | 40 | 缓冲达到该长度即发送 |
| max_interval | duration | 800ms | 距上次发送超过该时长即发送 |
| min_interval | duration | 800ms | 发送频率上限，超限合并到下次 |
| first_min_chars | int | 8 | 首段最小长度，避免先抛半个字 |
| edit_messages | bool | false | 用「编辑消息」表达改写；平台不支持时会中止该流 |
| typing_hint | string | 空 | 非空时首段正文前单发一条提示，如「正在输入…」 |

只在**直连模型**路径（agent.enabled=false）生效；ReAct 路径开着会在启动时告警并自动退回整段发送。

### 4.22 retrieval：历史召回

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| enabled | bool | false | 默认关闭：它改变 recall_history 的排序口径 |
| keyword_weight | float | 1.0 | 关键词一路权重；**负值 = 关闭该路** |
| vector_weight | float | 1.0 | 向量一路权重；负值 = 关闭该路 |
| top_k | int | 5 | 默认返回条数；工具给出的 limit 优先 |
| candidate_k | int | 20 | 每路候选数 |

摘要树（第三条独立召回源，需 retrieval.enabled）：

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| tree.enabled | bool | false | 是否启用摘要树召回 |
| tree.max_levels | int | 3 | 最多再构建几层 |
| tree.min_cluster | int | 2 | 成簇下限；小于 2 会退化成单链，配置侧回填 |
| tree.branching | int | 4 | 单簇节点上限（分支因子） |
| tree.max_nodes | int | 10000 | 全树节点上限 |
| tree.cluster_threshold | float | 0.5 | 归入既有簇的最低相似度 |

摘要用确定性拼接（不调模型），聚类用二值哈希向量；树按会话缓存，条目数变化时重建。

---
## 5. 架构

### 5.1 分层与依赖方向

```
cmd/server   组合根：读配置 → 装配 → 启动 → 关闭（唯一允许 import 全部 internal 包的地方）
   │
internal/*   库：只依赖比它更基础的库；不反向依赖 cmd/server
   │
stdlib + 少数白名单依赖（SQLite driver、yaml、websocket）
```

约定：除 cmd/server 外全部放 internal/；不在装配路径上的包不进仓库（F-79 的「未接线即负债」）。unsafe 只在 internal/unsafeutil 白名单。

### 5.2 启动与装配顺序

1. 解析 CLI → 读配置 → **校验**（一次性报告全部问题）→ 展开 `${VAR}` 密钥引用；
2. 打开 SQLite 并跑版本化迁移（打不开即退出）；
3. 构造日志、审计、指标、ops 监听（最后启动，保证探针读到的是已就绪状态）；
4. 构造 LLM（按 provider）、上下文预算、成本统计、提示词模板引擎（启动期校验）；
5. 加载权限表、人格目录（启动期校验）、构造记忆与 Agent、注册内置工具；
6. 装配路由（规则 + 三段钩子）、回复管道、出站出口、限速/单飞/开关等中间件；
7. 注册 bot 生命周期组件（会话 → 传输 → 存储），启动后台任务（会话回收等）；
8. 打印**由装配事实推出的能力清单**，开始监听。

### 5.3 消息流

```
OneBot 平台
   │ WebSocket 帧
   ▼
transport.WSClient ──► sink（读循环，禁止在此调用平台 API）
   │                        │
   │                        ├─ 会话级临时路由命中？──► 交给 Await 等待者
   │                        ▼
   │                  router.Engine.Dispatch（三段钩子：pre / mid / post）
   │                        │
   │        ┌───────────────┴────────────────┐
   │        ▼                                ▼
   │  reply 规则路由（命中即回复）       Always 兜底路由（只记录）
   │        └───────────────┬────────────────┘
   │                        ▼ 入队（有界，满则丢弃并告警）
   │                  reply worker 池（4 个 goroutine）
   │                        ▼
   │              internal/reply.Pipeline.Handle
   │   自动记忆 → 引用解析 → 记录用户轮次 → agent.Run → 用量台账 → 快照 → 分段/流式发送
   │                        ▼
   │                  agent.ReactAgent（ReAct 循环 + 工具）
   │                        ▼
   └──────────────  outbound.Sender（唯一出口：过滤链 + 审计）
```

要点：

- **所有消息都入队**，不只是被 @ 的：群聊里没被 @ 的内容是理解上下文的环境消息，按 token 预算压缩而非丢弃。
- **传输读循环里不做任何 API 调用**：OneBot 的响应只能由同一读循环读回，在那里 Call 必然死锁；引用解析因此放在 worker 里。
- **回复策略是路由规则**而不是 handler 里的分支：路由层就能回答「什么时候回复」。
- **唯一出口**：所有出站消息经 outbound.Sender，过滤与审计在一处完成。

### 5.4 提示词与缓存

提示词切成三段（F-65），顺序固定：

```
[静态段]   基础提示词 + 记忆指令 + 身份 + 工具提示      ← 由模板引擎渲染，进程启动期内逐字节稳定
[半静态段] 人格设定 + 权限表（按角色）                ← 只随人格/角色/权限表变化
[动态段]   记忆块 + 只追加历史 + 当前输入             ← 每轮变化
```

- **唯一装配点**：internal/conversation.Assembler。agent 经 MessageAssembler 接口注入装配器，绝不自己拼消息。曾经装配器只有测试在调用、生产各自拼装，后果是配置并写进日志的 ambient_token_budget 与呈现窗口从未生效——有回归测试断言「实际发出的请求逐条等于装配器输出」。
- **记忆位置**（ADR-0002）：记忆是独立消息，放在 system 之后、历史之前——既保住 system 段的全局缓存，又让记忆本身可被缓存。
- **高水位裁剪**：窗口成批滑动，两次移动之间完全稳定；逐轮滑动会让前缀每轮都变。
- **可回归**：/prompt-hash 报告各段哈希；prompt_snapshots 记录每轮指纹并区分「记忆变更（预期）」与「前缀意外分歧（告警）」；--stats 给出命中率。

### 5.5 持久化与数据分层（ADR-0003）

单一内嵌 SQLite，不开数据库服务端；表按**生命周期**分层：

| 层 | 内容 | 典型用途 |
|---|---|---|
| 消息流 | 归档消息与索引 | 全文检索、recall_history、导出 |
| 会话台账 | 会话元数据与用量归集 | /cost、--stats、会话回收 |
| 长期记忆 | 扁平记忆 + 分层（Working/Episodic/Semantic） | 记忆召回、遗忘、导出 |
| 在途状态 | 交互式等待、待审批 | 重启后恢复/通知 |
| 提示词快照 | 每轮提示词指纹与分段 | 缓存命中回归、前缀分歧告警 |

实现要点：单写者（Store.Write 串行化）、WAL、BEGIN IMMEDIATE、忙等默认 1s、schema 只在 internal/store/schema.go 声明并走版本化迁移；时间统一存 Unix 毫秒。JSONL 退化为导入/导出格式（history.file、agent.memory_file、--export-memories）。

### 5.6 记忆与检索

- **写入**（F-87）：规则触发 + 模型主动两条通道；写入决策用混合判据（确定性 + 歧义带问一次判官），判官故障不丢记忆。
- **分层**（F-49）：Working/Episodic 落独立的 tier 表；Semantic 复用扁平记忆表——不另写一套判定，遗忘/导出/去重语义一致，旧 JSONL 导入的也正是这一层。
- **召回**（F-51/F-52）：默认按关键词回溯；开启 retrieval 后改为 BM25 + 二值向量 + RRF 融合，摘要树作为独立的第三条召回源在摘要层检索。融合与树检索都只索引**对话轮次**（工具轮次与内部标记不是「聊过的内容」）。

### 5.7 权限与安全

- **传输鉴权**（F-80）：入站模式必须配 access_token，支持签名与 IP 白名单。
- **权限双层**（F-53/F-54）：提示词侧按角色渲染权限表（输入侧约束）+ 平台 API 出口硬拦截（执行侧）。拦截点包在 transport.Caller 上——那是所有平台动作的唯一出口，逐个调用点加判定一定会漏。未知角色 fail-closed。
- **密钥**（F-61）：env > file > inline；`${VAR}` 未设置即启动失败；--check-config 输出脱敏。
- **出站安全**（F-59）：统一 HTTP 客户端，禁止裸调用；白名单、超时、体积上限。
- **工具沙箱**（F-46）：策略层白名单与结构化拒绝（非 OS 隔离）。
- **出口过滤链**（F-55）：所有出站消息在一处过滤与审计。
- **审计**（F-60）：不可关闭；管理命令、封禁、审查拦截、审批、出站都留痕。

### 5.8 失败语义

| 场景 | 行为 |
|---|---|
| 配置非法 / 密钥缺失 / 资产非法 | 启动失败，非零退出（fail-closed） |
| 持久层打不开 | 启动失败，不降级为内存 |
| 模型调用失败 | 按重试与退避策略；最终失败在日志与指标里可见 |
| 摘要/判官/记忆写失败 | 退回确定性路径并告警，不丢主流程数据 |
| 队列满 / 审计写失败 | 丢弃并计数，绝不阻塞主流程 |
| 权限不通过 | 明确拒绝并回灌原因（不是静默忽略） |
| 关闭超时 | 按预算强制结束，避免卡死 |

### 5.9 热加载（F-24）

| 资产 | 是否热加载 | 语义 |
|---|---|---|
| 敏感词表 | 是 | 重编译失败保留旧表 |
| 人格目录 | 是 | 替换定义，只影响半静态段 |
| 权限表 | 是（文件存在时） | 换表即换缓存，判定立即生效 |
| 限速参数 | 是 | 监听配置文件本身，重建令牌桶；校验失败保留旧参数 |
| 提示词正文 / 模板 | **否（刻意）** | 启动时固定正是前缀缓存的前提 |

### 5.10 包地图

| 包 | 职责 |
|---|---|
| admin | 聊天内管理命令框架：注册、鉴权、审计、参数解析 |
| agent | Agent 契约、ReAct 循环、虚拟动作、审批闸门、记忆接口、范式包装 |
| audit | 审计事件与异步落盘 |
| backpressure | 有界事件队列（满则丢弃并告警） |
| bot | 生命周期编排：组件注册、在途等待、优雅关闭 |
| config | 配置 schema / 默认值 / 有效值访问器 / 校验 / 密钥 |
| conversation | 消息装配：三段前缀、记忆位置、呈现窗口、环境压缩 |
| cost | 成本归因、台账、配额 |
| event | 事件与消息模型、消息段、消息 ID |
| history | 对话历史接口 + SQLite 实现 + JSONL 导入 + 检索（混合/摘要树包装） |
| httpx | 出站 HTTP 安全客户端与 traceparent 注入 |
| imagehash | 感知哈希与图片描述缓存（F-62，边界受限，暂无消费方） |
| llm | LLM 接口、OpenAI 兼容实现、流式契约、重试、用量与计价、上下文预算 |
| memory | 长期记忆：作用域、分层、混合写入判据、判官、BM25 与向量检索 |
| metrics | 指标目录与暴露 |
| moderation | 入站审查（AC 自动机）、黑名单与防刷 |
| observe | 结构化日志与 trace_id |
| ops | /metrics /healthz /readyz 服务 |
| outbound | 出口过滤链与发送 |
| policy | 权限表、角色解析、判定缓存、渲染 |
| prompt | 提示词模板引擎（覆盖、启动期校验、行尾归一、渲染哈希缓存） |
| reload | 文件/目录变更监听的通用实现 |
| reply | 一次回复轮次：自动记忆、引用解析、台账、发送 |
| retry | 退避与重试 |
| router | 实例化路由注册表、规则、三段钩子、命令参数解析 |
| scope / scoped | 会话作用域 ctx key 与作用域内配置/人格 |
| secrets | 密钥文件与 `${VAR}` 解析 |
| semcache | 语义缓存（向量相似度 + 上下文指纹） |
| session | Session 与 Manager、临时路由、交互式等待 |
| store | SQLite 持久层：迁移、消息、台账、记忆、在途、快照 |
| testutil | 手写 Fake 与 golden 断言工具 |
| textguard | 敏感词 AC 自动机 |
| textsim | 文本相似度 |
| toggle | 按群功能开关 |
| tool / tool/builtin | 工具注册表、参数解析与内置工具集 |
| trace | W3C trace 上下文、采样 |
| transport | WebSocket 客户端、鉴权、Caller 抽象 |
| unsafeutil | unsafe 的唯一白名单目录 |
| vector | 二值/文本向量与索引 |

### 5.11 关键设计决策

| 主题 | 决策 | 记录 |
|---|---|---|
| 工具调用协议 | 原生 tool_calls 是唯一执行通道；文本动作解析降级为「抢救通道」 | [docs/adr/0001](docs/adr/0001-tool-call-protocol.md) |
| 记忆注入位置 | 记忆作为独立消息放在 system 之后、历史之前，同时保住 system 段的全局缓存 | [docs/adr/0002](docs/adr/0002-memory-injection-position.md) |
| 持久化分层 | 单一内嵌 SQLite；表按生命周期分层；JSONL 退化为导入导出格式 | [docs/adr/0003](docs/adr/0003-persistence-and-data-layering.md) |
| 消息装配 | conversation.Assembler 是唯一装配点，agent 不再自己拼消息 | 见 5.4 |
| 权限表位置 | 权限表按角色渲染，放在半静态段而非静态段：放进静态段会让前缀按角色分裂 | HANDOFF 已知偏离 |
| 能力清单 | 启动日志按装配事实生成，而不是手写字符串 | 见 5.2 |

---

## 6. 开发、测试与协作

### 6.1 常用命令

```sh
go build ./...                                   # 构建
go vet ./...                                     # 静态检查
go test ./...                                    # 跑测试
go test -race ./...                              # 竞态检测（CI 使用；本地需要 cgo）
go test -run=XXX -bench="." -benchtime=1x ./internal/... ./cmd/...   # 基准冒烟
gofmt -l cmd internal                            # 格式检查（应无输出）
```

> Windows 上基准必须写 -bench="."：PowerShell 会把 -bench=. 里的点当成包参数，报出看起来像「仓库坏了」的错误。

### 6.2 测试约定

- **Feature 编号进测试名**：Test_F30_Backoff —— 看到一个测试就能定位它覆盖哪条 Feature。
- **fake 优先**：核心链路单测不依赖网络、不 sleep；internal/testutil 提供手写 Fake。
- **接线必须证明效果**：测试要证明「行为发生了变化」，而不是「函数被调用了」。
- **契约测试**：流式契约、单动作端到端、多轮工具调用。
- **Golden 测试**：模板/权限表渲染逐字节比对；CI 中禁止 -update 重写。

### 6.3 CI

[.github/workflows/ci.yml](.github/workflows/ci.yml) 四类 job：

1. **build-test**：断言 go.mod 与 CI 的 Go 版本一致 → build → vet → golangci-lint → go test -race；
2. **golden-cross-platform**：Linux + Windows 矩阵，并断言 CI 中拒绝 -update；
3. **bench-coverage**：基准冒烟 + 覆盖率（低于阈值失败）；
4. 覆盖率产物上传。

### 6.4 Lint

golangci-lint v2 配置见 [.golangci.yml](.golangci.yml)，启用 errcheck / govet / staticcheck / gocritic / contextcheck / gosec / revive / forbidigo 等。几条硬规则：**库代码禁止 log.Fatal / os.Exit / panic / time.Sleep / 裸 http.Get**；unsafe 只在 internal/unsafeutil；错误一律 fmt.Errorf("...: %w", err)。

### 6.5 目录结构

```
AgentBot/
├── cmd/server/                组合根：main / serve / build / configmap / 各接线文件 / 维护命令
├── internal/                  全部库代码（见 5.10 包地图）
├── prompts/                   人格与模板资产（私有内容不入库，示例见 prompts/README.md）
├── docs/adr/                  架构决策记录
├── .scratch/agentbot/         规格与本轮 ticket（spec.md + issues/）
├── FEATURES.md                唯一功能规范（89 条）
├── HANDOFF.md                 接手文档：现状、已完成接线、已知偏离、工作方式
├── DEPENDENCIES.md            依赖准入清单（含许可证核对）
└── config.example.yaml        带注释的示例配置
```

---

## 7. 文档索引

| 文档 | 内容 |
|---|---|
| [FEATURES.md](FEATURES.md) | 唯一功能规范：89 条 Feature 的价值/规格/边界/验收 |
| [HANDOFF.md](HANDOFF.md) | 现状、已完成的接线与对应测试、已知偏离、每轮收尾流程 |
| [config.example.yaml](config.example.yaml) | 逐项注释的配置示例 |
| [docs/adr/](docs/adr/) | 架构决策记录（工具协议 / 记忆位置 / 持久化分层） |
| [.scratch/agentbot/spec.md](.scratch/agentbot/spec.md) | 本轮目标、已定决策、ticket 索引与里程碑 |
| [DEPENDENCIES.md](DEPENDENCIES.md) | 依赖准入清单与许可证核对 |
| [AGENTS.md](AGENTS.md) | 协作者（人或 AI）的入口约定 |

---

## 许可

MIT。第三方依赖及其许可证见 [DEPENDENCIES.md](DEPENDENCIES.md)。
