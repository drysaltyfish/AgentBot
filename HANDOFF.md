# 接手文档：FEATURES 剩余工作

> 用途：本会话上下文接近上限时留下的准确状态。**不是完成报告**——
> 目标"除多账号/多供应商路由外全部完成"尚未达成。

## 1. 目标与范围

- 目标（goal）：除 **F-07 多账号路由** 与 **F-27 多供应商路由** 外，FEATURES.md 中其余 Feature 全部完成。
- 规格唯一来源：FEATURES.md（89 条）。
- 仓库：Go 1.27.1，单二进制 + 内嵌 SQLite（modernc），OneBot v11 QQ 机器人。

## 2. 当前状态（干净、已推送）

- HEAD 见 `git log -1`；工作区干净，每轮本地 build/vet/test/gofmt 全绿，CI 走 `gh run watch`。
- **功能实现：87/89**；剩下两条（F-07 多账号、F-27 多供应商）在目标范围之外。
- **接线：原 §3.2 十项 + 收尾审计新增四项（F-32/F-53/F-54/F-33/F-79 清单）全部完成**。
  唯一未消费的库是 **F-62**（图片去重），它的消费方需要多模态请求，而没有任何 Feature
  定义这条上游能力（§4.7）。
- 收尾审计的方法：扫描"没有任何**非测试**代码 import 的 internal 包"。F-33/F-53/F-62
  就是这样找出来的——它们从未进过任何待办清单。
- **目标已收尾**：范围（除 F-07/F-27）内的功能与接线全部完成，唯一例外 F-62 经用户
  确认按"边界受限"记录（§4.7/§4.8），不作为未完成项。

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

| # | 项 | 入口 | 前置条件 / 风险 |
|---|---|---|---|
| C | F-62 感知哈希图片去重 | 图片入站链路 | 需要多模态请求（图片 → 视觉模型 → 描述），而 `llm.Message` 只有文本；FEATURES 里也没有任何一条定义这条上游能力。属边界受限，见 §4.7；要接就等于新增特性（改 provider 契约与全部 fake） |

收尾复扫（第 14 轮）结果：零生产引用的包只剩 `imagehash`（即 F-62）、`testutil`（测试专用）、
`unsafeutil`（白名单辅助）。同时修掉了 F-79 的能力清单过期问题（见 §2 表格）。

## 4. 已知偏离与诚实标注（不要当成"已完成"）

1. **F-72 无 OTLP 导出**：OpenTelemetry 不在依赖白名单，只实现了 traceparent 传播/采样/context。完整 OTel 需先走依赖准入。
2. **F-46 是策略层，不是 OS 级隔离**：没有 os/exec 独立进程、cgroup/Job Object、进程组强杀。接口（SandboxDeclarer）已留好。
3. **F-31/F-32 两处签名偏离规格**：JSONSchemaOf[T]() 返回 (ResponseFormat, error)；Fit/Count 带 ctx。理由已写入代码注释。
4. **F-66 的 `downgrade` 动作未强制**：按请求切模型需要 `llm.ChatRequest` 带 model 字段（协议形状改动）。当前会如实告警并继续用原模型。
   会话/用户维度的计量与 deny 已生效，快照落 SQLite。
5. **F-24 只交付敏感词表热加载**（组合根里唯一真正读文件的运行时资产）。
6. **F-63 自定义 Similarity 时退化为有界线性扫描**（包注释已写明理由与上限）。
7. **`llm.max_iterations` 是空转字段**：它会被校验（<=0 报错）并出现在示例配置里，但全仓库没有消费方；
   工具调用轮数实际由 `agent.max_iterations` 控制。已在 README 与示例配置里如实标注，
   保留字段是为了兼容既有配置；要真正生效需要先定"LLM 客户端自己循环工具调用"这套设计。
8. **权限表落在半静态段，而不是 F-65 说的"静态段"**：F-65 的静态段清单里列了权限表，
   但 F-53 的权限表是**按角色**渲染的——放进静态段会让前缀按角色分裂，与"进程启动期内
   逐字节稳定"直接冲突。取舍是：静态段仍逐字节稳定，权限表与人格并列进半静态段。
   这是两条规格的冲突，不是遗漏；收尾审计才发现它需要显式裁定。
8. **F-62 只有库，没有消费方**（**已与用户确认按"边界受限"收尾**）：感知哈希、去重缓存、分桶都完整且有测试，但消费方需要
   "图片 → 视觉模型 → 描述"这条链路，而仓库当前不支持多模态请求（`llm.Message` 只有文本），
   供应商路由 F-27 也在目标范围外。接入需要先扩多模态消息形状——那是新特性，不是接线。
8. **F-65 的静态段报告只覆盖 system 正文**：工具 schema 随请求的 Tools 字段发送，
   顺序由 F-41 的注册顺序固定，不在 `/prompt-hash` 的 static 段里。
   另：`DefaultPersona` 不注入半静态正文（内置 default.yml 与 `DefaultSystemPrompt`
   逐字相同，再注入一次等于每个请求发两遍同一段话）。

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
