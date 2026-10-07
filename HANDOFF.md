# 接手文档：FEATURES 剩余工作

> 用途：本会话上下文接近上限时留下的准确状态。**不是完成报告**——
> FEATURES 范围（除 F-07 多账号 / F-27 多供应商）的功能已收尾，
> 但**未接线台账（§4.17）与已知偏离（§4）需要持续维护**，且当前工作区**有未提交改动**。

## 1. 目标与范围

- 目标（goal）：除 **F-07 多账号路由** 与 **F-27 多供应商路由** 外，FEATURES.md 中其余 Feature 全部完成。
  该目标**已收尾**（见 §2）。
- **其后的若干轮是质量优化与审计**，目标是「完全优化」：修缺陷、给装配加契约、
  把"实现了但没接线"的项盘成清单（§4.17）。**这批改动尚未提交**（见 §2）。
- 规格唯一来源：FEATURES.md（89 条）。
- 仓库：Go 1.27.1，单二进制 + 内嵌 SQLite（modernc），OneBot v11 QQ 机器人。

## 2. 当前状态（功能已收尾；优化轮次的改动**尚未提交**）

- HEAD 见 `git log -1`。**工作区当前有未提交改动**：约 40 个文件修改 + 25 个新文件
  （质量优化轮次的产物，逐项见 CHANGELOG「未发布」）。
  每轮本地 `build/vet/test/gofmt/--check-config` 全绿；CI 走 `gh run watch`。
- **功能实现：87/89**；剩下两条（F-07 多账号、F-27 多供应商）在目标范围之外。
  **但这个口径要看清**：87/89 是按 **Feature** 计的，判据是**验收**通过，
  不是"规格里每个字都做了"。最典型的反例是 F-04——它的验收只要求"内存 mock 传输跑得通"，
  已通过；而它规格里列的 `wsserver`（反向 WS）与 `http`（HTTP 上报）两个驱动**根本没实现**。
  所以**不能从 87/89 推断某个配置项可用**。详见下面两条警告。
- **接线**：原 §3.2 十项，加上收尾审计另外找出的 F-53/F-54 与 F-33，全部完成；
  F-32、F-79 也在同一轮完成。逐项内容与证明它的测试见下表。
- **⚠️ "接线全部完成"这个结论方法上偏窄，别再照抄**：当年的收尾审计只扫
  "`internal/*` 里没有任何**非测试**代码 import 的包"。它能找出**整包**没人用
  （F-33/F-53/F-62 就是这样找出来的，且从未进过任何待办清单），
  却**看不见已被 import 的包里那些没人调用的能力**。
  更细的扫描（找"符号去掉注释后只在定义处出现"）又找出六处，
  其中入站 guard 链、出站重试、F-32 的实测计数都有实际影响。
  **未接线台账以 §4.17 为准，不要只看本条。**
- **目标已收尾**：范围（除 F-07/F-27）内的功能与接线按上述口径完成，唯一例外 F-62
  经用户确认按"边界受限"记录（§4.10），不作为未完成项。

### 已完成的接线

| Feature | 接线内容 | 证明它的测试 |
|---|---|---|
| F-57 / F-58 | UsePre 挂入站审查；拦截进审计与 guard_blocks 指标 | cmd/server 相关测试 |
| F-82（半） | prompt.personas_dir + 启动期 scoped.Load/ValidateRefs | 目录不存在时不失败（已确认 persona.go 语义） |
| F-24（切片） | 敏感词表热加载（watch -> 重编译 -> SwapMatcher） | Test_F24_SensitiveWordsHotReload（端到端证明"生效"） |
| F-46 | sandbox 分节 + registry.Sandbox（装配最后一步，保序） | Test_F46_SandboxPolicyFromConfig（映射 + 启动期校验） |
| F-66（半） | cost 分节 + observedLLM 按真实 usage 记账 + `/cost` 命令 | Test_F66_ObservedLLMRecordsCost、Test_F66_AdminCostCommandReportsUsage |
| F-71 | admin 模块（/help、/ban、/unban、/banlist）+ 超管鉴权 + 审计 | Test_F71_AdminModuleAuthorizesAndAudits |
| F-36 / F-37 | LLM Evaluator + `agent.paradigm`（reflexion / orchestrator） | Test_F36_ReflexionParadigmDrivesEvaluation（断言评估器被调用 2 次） |
| F-65 | 三段式前缀（静态/半静态/动态）+ `/prompt-hash` 接线 | Test_F65_StaticSegmentIsByteStableAcrossDynamicInputs、Test_F65_HalfStaticTracksPersonaNotScope、Test_F65_PromptHashWithoutPersonaStillReportsStatic |
| F-82（剩余） | SQLite 会话人格持久化 + `/persona` 切换 + RouteKey/Fingerprint 喂半静态段 | Test_F82_PersonaPersistsAcrossReopen、Test_F82_PersonaSwitchChangesPromptHash、Test_F82_DefaultPersonaDoesNotDuplicateTheStaticSegment |
| F-23 | 定期回收接入 Bot 生命周期（`app.Go`，不再是没人启动的 `StartReclaimer`） | Test_F23_ReclaimerIsOwnedByBotLifecycle（100 会话回收 + Shutdown 后回收器确实停止） |
| F-49（持久化） | `memory.SQLiteTierStore`：分层记忆落 SQLite（id 走 tier_ids 统一分配器，scope 全程参与） | Test_F49_SQLiteTierStoreSurvivesReopen、Test_F49_TieredMemoryOverSQLiteEndToEnd |
| F-63 | 语义缓存接在**回复链路**（会话键 + 人格指纹 + 出口过滤 + 历史一次到位）；`vector.TextBinary` 提供二值哈希向量化；命中/未命中/省下 token 进指标 | Test_F63_SecondIdenticalQuestionSkipsTheModel、Test_F63_ToolTurnsAreNotCached、Test_F63_CacheHitGoesThroughTheSendChain、Test_F63_BuildSemcacheFollowsConfig |
| F-48（归属 + 反思） | 记忆加 `subject_id`：归属（谁的事）与作用域（谁看得到）分离，`memory_save` 支持 `shared` 记公共记忆，`memory_recall` 支持 `subject_qq` 按人查；新增空闲反思（`internal/reflect` + `internal/memory/reflect.go`），四道闸门 + 关闭思考模型 + 输入压缩，默认关闭 | Test_F87_SubjectIsolatedWritesAndScopedRecall、Test_F87_RecallForKeepsSharedMemories、Test_F48_ReflectOnlyWhenIdle、Test_F48_ReflectThrottledPerSession、Test_F48_WatermarkSkipsNothingNew、Test_F48_SharedAndPersonalFactsAreWrittenWithSubject |
| F-33 | 静态前缀改由模板引擎渲染（`prompt.dir` 同名模板覆盖内置版本、启动期校验、CRLF 归一）；内置 `system.tmpl` 去掉"当前时间"——F-65 不允许静态段含易变内容，时间已在轮次渲染里出现。**这是资产形状变更**：用旧字段（BotName/Persona/Tools）覆盖 system 模板的部署需同步改 | Test_F33_RenderedPrefixIsByteStable、Test_F33_CRLFAndLFOverrideRenderIdentically、Test_F33_TemplateErrorsFailAtStartup |
| F-53 / F-54 | 权限表加载 + 提示词半静态段渲染（按角色）+ 平台 API 出口硬拦截（`policyMiddleware`，fail-closed）；角色经 ctx 传递、超管优先；权限表文件存在时热加载 | Test_F53_MiddlewareDeniesUnauthorizedAction、Test_F53_MiddlewareFailsClosedWithoutRole、Test_F53_PromptProviderRendersRoleTable、Test_F53_PolicyStateSwapTakesEffect |
| F-79（能力清单） | 启动日志的能力清单改为**由装配事实推出**（`assembleCapabilities` + `capabilityInputs`）：此前是一份手写字符串，早已过期（不含 persona/policy/模板/缓存/流式/追踪/成本/记忆/检索）。现在 20+ 项按真实对象判定，重复项去重 | Test_F79_CapabilityListReflectsActualWiring |
| F-32 | 上下文预算接入 `observedLLM`（调用前 `FitRequest`，工具 schema 计入预算）；`conversation.Assembler` 把 system 标为 `Pinned`，否则裁剪会丢系统提示词 | Test_F32_ObservedLLMTrimsBeforeCallingProvider、Test_F32_StreamingPathIsTrimmedToo、Test_F32_AssemblerMarksSystemPinned |
| F-24（限速） | 限速改为「规则持有者 + 原子替换」：mid 规则启动时注册一次、内部解引用当前参数，热加载只需一次原子写；监听**配置文件本身**，校验失败保留旧参数 | Test_F24_RateLimitStateSwapsAtomically、Test_F24_RateLimitHotReloadFromConfigFile |
| F-52 | 摘要树作为**第三条独立召回源**接入 `history.Hybrid`（摘要层检索，与 BM25/向量并列而非硬融合）；确定性拼接摘要 + 二值哈希聚类；按会话缓存、条目数变化重建 | Test_F52_TreeIsAnIndependentRecallSource、Test_F52_TreeRebuildsWhenHistoryGrows、Test_F52_WrapHistoryEnablesSummaryTree |
| F-51 | `history.Hybrid`：BM25 + 二值向量 + RRF 融合，包装历史存储即生效；`recall_history` 靠既有 Searcher 断言自动改走检索；只对对话轮次建索引 | Test_F51_HybridSearchFindsKeywordHitWithContext、Test_F51_HybridSkipsNonConversationalEntries、Test_F51_WrapHistoryWithRetrievalFollowsConfig |
| F-49（切换） | 分层记忆上线：Working/Episodic 落 tier 表，**Semantic 复用 F-87 扁平记忆表**（不另写判定）；`TieredMemory` 实现 `MemoryAdmin`；直连路径也开始注入记忆 | Test_F49_SemanticLayerReusesF87Dedup、Test_F49_TieredMemoryAdminForgetAndList、Test_F49_BuildAgentWiresTieredMemory、Test_F49_DirectAgentInjectsMemory |
| F-24（人格目录） | `watchPersonas` 监听人格目录热替换定义；`reload` 指纹支持目录（逐子文件，避免"改已存在文件不触发"） | Test_F24_PersonaFileChangeSwapsDefinitions、Test_F24_ReloadsDirectoryContentChange |
| F-72（传播） | W3C trace 上下文：入站事件建立 span、日志 trace_id 与之一致、httpx 自动带 traceparent | Test_F72_TraceparentIsPropagated、Test_F72_EventTraceContextCarriesW3CIdentity、Test_F72_ExplicitHeaderIsPreserved |
| F-66（剩余） | 会话/用户维度归因经 ctx 进 LLM 链、调用前强制 deny、SQLite 快照持久化、`/cost` 报本会话 | Test_F66_QuotaDenyHappensBeforeSpending、Test_F66_SessionQuotaIsPerSession、Test_F66_CostSnapshotPersistsThroughAdapter、Test_F66_QuotaDenialRepliesWithANotice |

## 3. 剩余工作

### 3.1 功能（0 条）

- ~~**F-23 会话生命周期与回收**~~ **已接线并验收**：`startSessionReclaimer` 用 `app.Go`
  把定期回收挂到 Bot 的生命周期上（此前 `Manager.StartReclaimer` 在组合根从未被调用，
  等于定期回收在生产里不存在）。验收测试见 §2 表格。
  "固化记忆"不需要额外钩子：记忆在写入时已落 SQLite（F-87），回收没有待刷的数据。
- ~~**F-65 提示词前缀稳定化**~~ **已完成**（21059e7）。

范围内的功能实现全部完成。剩余工作只有 §3.2 的接线；
F-07（多账号）与 F-27（多供应商）明确在目标范围之外。

### 3.2 接线

第一轮列出的十项全部完成：
~~#1 admin 命令入口~~、~~#2 cost 会话维度~~、~~#3 agent.paradigm~~、~~#4 分层记忆~~、
~~#5 semcache~~、~~#6 tree 摘要树~~、~~#7 stream->outbound~~、~~#8 trace~~、
~~#9 reload 其余资产~~、~~#10 F-82 剩余~~。

**收尾审计新增（原先完全没进台账）**：用"`internal/*` 里没有任何非测试代码 import 的包"
做扫描，找出三处库就绪但无人消费的能力；其中 **F-53/F-54**（权限即提示词 / 硬拦截）
与 **F-33**（提示词模板引擎）已接入。仅剩一项：

> ⚠️ **这张表只覆盖"整包没人 import"这一类。** 包内"实现了但没有任何生产调用方"的能力
> 另有一批（入站 guard 链、出站重试/限流、F-32 实测计数、F-31 结构化输出、前缀快照的读路径…），
> 完整清单与**失效判据**见 **§4.17**。要判断"某项到底生效没有"，以 §4.17 为准。

| # | 项 | 入口 | 前置条件 / 风险 |
|---|---|---|---|
| C | F-62 感知哈希图片去重 | 图片入站链路 | 需要多模态请求（图片 → 视觉模型 → 描述），而 `llm.Message` 只有文本；FEATURES 里也没有任何一条定义这条上游能力。属边界受限，见 §4.10；要接就等于新增特性（改 provider 契约与全部 fake） |

收尾复扫（第 14 轮）结果：零生产引用的包只剩 `imagehash`（即 F-62）、`testutil`（测试专用）、
`unsafeutil`（白名单辅助）。同时修掉了 F-79 的能力清单过期问题（见 §2 表格）。

## 4. 已知偏离与诚实标注（不要当成"已完成"）

1. **F-72 无 OTLP 导出**：OpenTelemetry 不在依赖白名单，只实现了 traceparent 传播/采样/context。完整 OTel 需先走依赖准入。
2. **F-46 是策略层，不是 OS 级隔离**：没有 os/exec 独立进程、cgroup/Job Object、进程组强杀。接口（SandboxDeclarer）已留好。
3. **F-31/F-32 两处签名偏离规格**：JSONSchemaOf[T]() 返回 (ResponseFormat, error)；Fit/Count 带 ctx。理由已写入代码注释。
4. **F-66 的 `downgrade` 动作未强制**：按请求切模型需要 `llm.ChatRequest` 带 model 字段（协议形状改动）。当前会如实告警并继续用原模型。
   会话/用户维度的计量与 deny 已生效，快照落 SQLite。
5. **F-24 的热加载资产与限制**：敏感词表、人格目录、权限表、限速参数均热加载；
   **提示词正文与模板刻意不热加载**——启动时固定正是前缀缓存（F-65）的前提。
6. **F-63 自定义 Similarity 时退化为有界线性扫描**（包注释已写明理由与上限）。
7. **F-58 扩展：名单与角色（access 分节）**：在既有 moderation 黑名单之外，新增了**路由层直接丢弃**的名单
   （access.mode = allow/deny，按 QQ 号与群号）与**用 QQ 号显式指定角色**（access.roles）。
   与 F-58 的分工：名单只看得到 QQ/群号，最彻底；需要看内容的审查仍在 moderation。
   超管名单**只有一处**：access.roles.superuser。旧的 moderation.super_users 已**移除**：
   该键出现即启动失败并给出迁移指引（不静默忽略——否则会表现成"我明明是超管，命令却不管用"）。
   影响面：路由 pre 钩子、policy 角色解析、审批角色（agentRoleFor）、/switch 授权。
8. **`llm.max_iterations` 是空转字段**：它会被校验（<=0 报错）并出现在示例配置里，但全仓库没有消费方；
   工具调用轮数实际由 `agent.max_iterations` 控制。已在 README 与示例配置里如实标注，
   保留字段是为了兼容既有配置；要真正生效需要先定"LLM 客户端自己循环工具调用"这套设计。
9. **权限表落在半静态段，而不是 F-65 说的"静态段"**：F-65 的静态段清单里列了权限表，
   但 F-53 的权限表是**按角色**渲染的——放进静态段会让前缀按角色分裂，与"进程启动期内
   逐字节稳定"直接冲突。取舍是：静态段仍逐字节稳定，权限表与人格并列进半静态段。
   这是两条规格的冲突，不是遗漏；收尾审计才发现它需要显式裁定。
10. **F-62 只有库，没有消费方**（**已与用户确认按"边界受限"收尾**）：感知哈希、去重缓存、分桶都完整且有测试，但消费方需要
   "图片 → 视觉模型 → 描述"这条链路，而仓库当前不支持多模态请求（`llm.Message` 只有文本），
   供应商路由 F-27 也在目标范围外。接入需要先扩多模态消息形状——那是新特性，不是接线。
11. **F-65 的静态段报告只覆盖 system 正文**：工具 schema 随请求的 Tools 字段发送，
   顺序由 F-41 的注册顺序固定，不在 `/prompt-hash` 的 static 段里。
   另：`DefaultPersona` 不注入半静态正文（内置 default.yml 与 `DefaultSystemPrompt`
   逐字相同，再注入一次等于每个请求发两遍同一段话）。
12. **角色解析有两套实现，生产只用其中一套**（架构评审发现，尚未处理）：
   - 线上：`access.roleForEvent` → `policy.WithRole`/`RoleFrom`。角色在事件入口解析一次、
     放进 ctx，出口只从 ctx 读。数据源是**事件里平台已上报的** `Sender.Role`
     加上 `access.roles` 的显式指定，不需要额外查询。
   - 未接线：`policy.RoleResolver`/`policy.MemberLookup` 与
     `router.GroupAdmin`/`GroupOwner`/`HigherThan`/`router.MemberLookup`。
     它们要求宿主按 QQ 去平台查群成员角色。全仓库没有非测试调用方。
   `cmd/server/policy.go:53-54` 明确写了组合根**刻意**不用 `RoleResolver`：
   "这里不再单独实现一份，避免出现两处判定彼此漂移"。
   当前处理方式是**在两处声明上加"当前无生产调用方"注释**，并保留代码——
   与 F-62 同样按"有库无消费方"对待。要删除的话，涉及 policy.go 与 router/rules.go
   各约 40–60 行加两份测试；要接线的话，先回答"事件里已经有角色，为什么还要多查一次"。

13. **`internal/moderation` 有三个"只实现了库、没接线"的部分**（架构审计发现，尚未处理）：
    - **guard 链**（`GuardChain` / `RuleGuard` / `LLMGuard` / `VectorGuard`）：
      组合根 `buildModeration` 构造 `moderation.Options` 时**没有填 `Guards`**，
      于是 `Engine.chain` 恒为 nil，这四个类型在生产里一次都不会跑。
      **线上真正生效的入站审查是：黑名单 → 防刷 → 敏感词自动机（F-56）**。
      接线的前提是先有配置面（规则表 / 会花钱的分类器 / 恶意语料索引），
      那是新特性而不是接线，所以按 F-62 的先例记为"有库无消费方"。
    - **`AmbientPolicy`**：`Options.Ambient` 同样无人填，背景消息与正式对话走同一套策略。
      这不降低安全性（更严格），只是差异化能力没被使用；配置里也没有对应开关。
    - **`Commander`**（/ban、/unban、/banlist）：线上走 `admin.Module`
      （`cmd/server/observability.go` 注册），它直接调 `Blacklist`。
      两套实现留哪一套需要先决定，所以先标注不删除。
    处理方式与第 12 条一致：**在声明处加"当前无生产调用方"注释**，保留代码。

14. **传输层的重连已经接上，但它走的是组合根而不是 F-04 的 `RetryDriver`**：
    `cmd/server/wssession.go` 的 `runWSSession` 负责"连接 → 读循环 → 退避重连"，
    退避复用 `retry.Default().Delay`（F-30）。
    之所以没直接用 `transport.RetryDriver`：那是 **Driver 级**装饰器，而这里的循环
    还要在断开时调 `OnDown`（把 readiness 置回 false）、在重连前调 `Disconnect`。
    两件事 `RetryDriver` 都管不了，硬套只会把状态更新藏进装饰器里。
    **`RetryDriver`（driver.go）与 `RetryCaller` / `RateLimitedCaller` / `RecordingCaller`
    （caller.go）目前仍无生产调用方**，与第 12、13 条同样按"有库无消费方"对待。
    其中 **`RecordingCaller` 的"重试 send_msg 会重复发消息"是真实的取舍**：
    出站调用目前是 fail-fast，没有重试也没有限流，接线前必须先决定重复投递怎么处理。
    另外这次修的是一个**真缺陷**：修复前 `ws-session` 里一次 `Connect` 失败就 `return`，
    进程还活着、`/readyz` 还报 ready（`wsUp` 只被置 true、从不回 false），
    而机器人永远收不到消息，没有任何自愈路径。

15. **入站传输模式 `wsserver` / `http` 未实现，已改为启动前拒绝**（本轮修复）：
    全仓库只有 `WSClient`（正向 WS）一个 Driver 实现，组合根也无条件按 wsclient 建连接，
    但 `validate.go` 过去只校验 `access_token`，于是这两个值能通过校验。
    实测后果：`--check-config` 返回 **0**，进程打印 `agentbot started`（`transport=wsserver`），
    然后拿着**空 URL** 无限重连（`dial : failed to WebSocket dial`）——
    与真正的网络故障无法区分，而且唯一的排障工具给了绿灯。
    现在 `transport.mode` 直接报错拒绝启动，README 的对应两处也已改正。
    **恢复这两种模式时要一并加回**「入站必须配 `access_token`」（F-25 / F-80）那条检查。
    真正的实现工作是两个 Driver（反向 WS 多连接、HTTP 上报 + `AuthorizeHTTP`），
    属于新特性而不是接线，所以没有在本轮顺手实现。

16. **`httpx.SecureJoin` / `ImageSize` / `CheckImagePixels` 只有测试调用方**（本轮核实）：
    这不是"漏了路径穿越防护"，而是**根本没有需要保护的路径**——
    全仓库没有一个内置工具碰文件系统：10 个内置工具都不接路径参数，
    `internal/tool/builtin` 里没有 `os.Open` / `os.ReadFile` / `filepath.` 的任何调用。
    这与 README 里「沙箱四个白名单键不生效」是**同一个根因**，
    两个方向互为佐证：没有接路径的工具，所以既不需要 root 检查，也没有名字可拼。
    已在 `SecureJoin` 上写明"当前没有生产调用方"，并注明将来接路径类工具时
    **必须同时**实现 `tool.SandboxDeclarer` 与调用本函数，只做后者没有意义。

17. **未接线清单（汇总）**：第 12–16 条加上本轮审计的结果，统一列在这里，
    判据是**"它失效时会怎样"**。改代码前请先看这张表。

    **A 类：文档或规格会让人以为它已生效（危险）**——失效是静默的：

    | 能力 | 线上实际生效的是什么 |
    |---|---|
    | `moderation` guard 链（`GuardChain` / `RuleGuard` / `LLMGuard` / `VectorGuard`） | 只有**黑名单 → 防刷 → 敏感词自动机**（`Options.Guards` 无人填） |
    | `moderation.AmbientPolicy` | 背景消息与正式对话走同一套策略（更严格，不降低安全性；`Options.Ambient` 无人填） |
    | `transport.RetryCaller` / `RateLimitedCaller` / `RetryDriver` | 出站平台调用是 **fail-fast**：无重试、无限流。`RetryCaller` 已改成**只重试幂等动作**（`RetryableActions`，见第 35 轮），将来接线也不会造成重复投递。`RecordingCaller` 同样无生产消费方，且它**只记录消息 ID、拿不到内容**——见第 18 条 |
    | `llm.JSONSchemaOf` / `ChatStructured` / `ApplyFormatFallback` | **F-31 结构化输出实现完整但没有消费方**（只有 `DecodeStructured` 有 1 处调用，且那处也在 `structured.go` 内部）；工具参数仍靠提示词 + 文本 JSON 解析。**决策已记入 [ADR-0004](docs/adr/0004-structured-output-not-wired.md)**：不接线，先加指标观测 |
    | `store.CountDivergedSnapshots` / `ListPromptSnapshots` | 前缀快照**只写不读**：没有查询入口也没有对应指标，"前缀从哪一轮开始不稳"这个问题目前答不了 |
    | `policy.RoleResolver` / `router.GroupAdmin` / `GroupOwner` / `HigherThan` | 角色由 `access.roleForEvent` 从事件解析一次放进 ctx（见第 12 条） |
    | `transport` 的入站模式 `wsserver` / `http` | 未实现，已在启动前**直接拒绝**（见第 15 条） |

    **B 类：宿主可用的内核 API（没有消费方，但也没有失败模式）**——保留，不必接线：
    `imagehash`（F-62）、`httpx.SecureJoin` / `ImageSize` / `CheckImagePixels`（第 16 条）、
    `router` 的规则 DSL（`OnCommand` / `OnPrefix` / `Regex` / `UseRules` / `Ctx.Get*`，只有测试在用）、
    `moderation.Commander`、`session.Manager.StartReclaimer`、`session.Await`、
    `llm.ComparePrefix`（由 `store.compareDigest` 承担，见第 31 轮的字面量耦合测试）、
    `agent.ParamString` / `ParamInt64` / `ParamRaw`。
    其中 `Commander` 与 `StartReclaimer` 属于**组合根另有实现**：
    线上分别是 `admin.Module` 与 `cmd/server` 里的 `startSessionReclaimer`。

    接线 A 类之前要先确认那是**新特性**还是真的漏接。例如出站重试会引入
    "同一句回复发两遍"的取舍，不能顺手加上。

    **已在本轮接线的一项**：`llm.MeasuredCounter`（F-32 的"provider 实测优先"）。
    `cmd/server/budget.go` 的 `buildBudget` 现在返回 `budgetWiring{Budget, Counter}`，
    两者共用**同一个**计数器；`observedLLM.Chat` 在每次成功响应后
    `Observe(o.model, req.Messages, resp.Usage)`。三个容易写错的点都钉在测试里：
    - 各建一个计数器 → "接上了"与"没接"表现完全一样（预算永远停在启发式）；
    - 记成裁剪**前**的消息序列 → 缓存键与真实请求对不上，效果为零；
    - 流式路径**仍未覆盖**：`ChatStream` 这个位置拿不到 usage，
      所以流式请求不参与校准（流式默认关闭，F-64）。

18. **F-55 的验收点名了一个做不到这件事的仪器**（第 39 轮发现并补齐）：
    F-55 的第一条验收写"断言所有发送路径都经过过滤链（**用 `RecordingCaller` 校验内容已被处理**）"，
    但 `transport.RecordingCaller` 记录的是 `event.ID`——**消息 ID，不是消息内容**，
    因此它无法校验"内容已被处理"。规格在这里指的应该是"一个能记录发往平台的请求的替身"，
    而不是那个具体的内置类型（它自己的用途是"记录发出的消息 ID"，见 F-05）。

    补齐方式是两条，都做过反证：
    - **运行时**（`internal/reply/exit_filter_test.go`）：用一个记录**完整请求**的测试替身，
      断言真正发往平台的文字内容带着过滤链的痕迹（覆盖正常回复与语义缓存命中两条路径）；
    - **结构性**（`cmd/server/outbound_paths_test.go`）：扫描生产源码，确认除 `internal/outbound`
      之外没有任何包调用 `transport.SendGroupMsg` / `SendPrivateMessage` 这类发送包装函数。
      前者只能覆盖它跑到的路径；后者防的是**新增一条绕过唯一出口的路径**——
      那时旧测试全绿，而用户内容已经绕过过滤。
    - 只读型平台调用（`get_group_member_info` / `get_stranger_info` / `get_msg` 等）不在检查范围内：
      它们不产生发给用户的文本，出口过滤与它们无关。这条边界写在测试注释里。

    **同源问题**：`internal/reply` 里旧测试 `Test_F63_CacheHitGoesThroughTheSendChain`
    把 filter 注册成 `"suffix"`，而 `Chain.Apply` 只按固定的 `Order` 遍历**已知位置**——
    那个 filter 从未执行过，测试却一直是绿的（它只断言了发送次数）。
    已改为 `outbound.FilterNormalize` 并写明原因。**注册新名字的 filter 是静默无效的。**

19. **`transport.RetryDriver` 是陷阱，不是可用的重连实现**（第 40 轮查清，已写进代码注释）：
    F-04 说"连接失败的重试策略外置：`RetryDriver{next, backoff}`"。但把仓库里这个装饰器接上会**更糟**：
    - **`Listen` 不重新 `Connect`**：连接断开后它只是在同一条死连接上再读一次，
      徒劳地重复 MaxAttempts 次然后放弃。重连的本质是"把连接重新建起来"。
    - **尝试次数有上限**：配 `retry.Default()` 只有 **3 次**。F-04 的措辞
      "默认 1s 起、最长 30s、带 jitter" 描述的是**退避上限**，隐含"一直重试、退避封顶"；
      而 3 次之后永久放弃正是第 23 轮修掉的那个缺陷形态（进程在、探针 ready、机器人失聪）。

    线上真正在用的是组合根的 `runWSSession`（`cmd/server/wssession.go`）：
    `Connect → Listen → Disconnect → 再 Connect`，**不设尝试上限**，只复用 `Delay` 的退避与封顶。
    两条陷阱都有测试钉住（`internal/transport/driver_retry_trap_test.go`），
    其中一条断言"`Listen` 重试期间 `Connect` 一次都没被调用过"——若那天它变成非 0，
    说明 `RetryDriver` 学会了重连，该更新这条与代码注释。

## 5. 工作方式约定（务必遵守）

- **本地跑不了 golangci-lint 与 -race**（无 cgo/gcc）；两者由 CI 把关。这意味着
  "本地全绿"不等于能过 CI——已多次在 CI 才暴露（-race 下的预算断言、
  粗粒度时间戳导致的指纹漏检、queueWriter 关停竞争）。
- 每轮收尾流程：go build ./... && go vet ./... && go test -count=1 ./... &&
  go test -run=XXX -bench="." -benchtime=1x ./internal/... ./cmd/... && gofmt -l cmd internal
  （Windows 上必须写 -bench="."：PowerShell 会把 `-bench=.` 的 `.` 当成包参数，
  症状是 "FAIL . [setup failed]" / "no Go files in <repo>"，看起来像仓库坏了。）
  -> 通过才提交 -> git push origin main -> gh run watch <id> --exit-status。
- **基准必须显式跑**：本地 `go test ./...` 不跑基准，而 CI 的 bench smoke 跑。
  第 13 轮就因漏了这步，CI 才暴露 `BenchmarkRenderPrompt` 的数据字段过期。
- 提交信息用中文，说明"为什么"而不只是"改了什么"；偏离规格必须写进提交与代码注释。
- **每处接线都要带一个能证明"效果"的测试**，而不是"函数被调用了"。
  反例：paradigm: reflexion 在 Evaluator 为 nil 时接了也没效果。
- 子代理写文件时**不要写含真实换行的 Go 字符串字面量**（本会话多次被此坑到，
  症状是 string literal not terminated；修法是按换行数还原转义）。
- 批量字符串替换后**立刻校验**（grep 确认目标文本确实变了）。本会话有一次
  打印了"成功"但替换没匹配上，导致后续按不存在的代码定位、连续失败两轮。

## 6. 值得保留的既有资产

- .scratch/agentbot/spec.md：已完成批次记录与决策。
- README.md：功能状态表（注意其"已完成"计数未区分"库就绪"与"已接线"，
  接手后建议在表里加一列）。
- .github/workflows/ci.yml 的 golden 步骤：探测改为扫描已提交的 *.golden
  （此前用 go test -list 在 ubuntu/windows 结果不一致，等于一半的假安全感）。
