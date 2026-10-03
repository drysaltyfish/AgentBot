# 接手文档：FEATURES 剩余工作

> 用途：本会话上下文接近上限时留下的准确状态。**不是完成报告**——
> 目标"除多账号/多供应商路由外全部完成"尚未达成。

## 1. 目标与范围

- 目标（goal）：除 **F-07 多账号路由** 与 **F-27 多供应商路由** 外，FEATURES.md 中其余 Feature 全部完成。
- 规格唯一来源：FEATURES.md（89 条）。
- 仓库：Go 1.27.1，单二进制 + 内嵌 SQLite（modernc），OneBot v11 QQ 机器人。

## 2. 当前状态（干净、已推送）

- HEAD = 21059e7，已推送；工作区干净，本轮本地 build/vet/test/gofmt 全绿，CI 走 `gh run watch`。
- **功能：87/89 完成**；剩下两条（F-07 多账号、F-27 多供应商）在目标范围之外。
  范围内全部完成：F-65 三段式前缀（21059e7）、F-23 定期回收接线（本轮）。
- **接线：12/15 完成**（含 F-36/F-37、F-71、F-65、F-82、F-23，以及本轮补齐的 F-66 会话维度与持久化）。这是最大的缺口：按仓库自己的 **F-79**，未接线 = 未交付。

### 已完成的接线（7 处）

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
| F-66（剩余） | 会话/用户维度归因经 ctx 进 LLM 链、调用前强制 deny、SQLite 快照持久化、`/cost` 报本会话 | Test_F66_QuotaDenyHappensBeforeSpending、Test_F66_SessionQuotaIsPerSession、Test_F66_CostSnapshotPersistsThroughAdapter、Test_F66_QuotaDenialRepliesWithANotice |

## 3. 剩余工作

### 3.1 功能（0 条）

- ~~**F-23 会话生命周期与回收**~~ **已接线并验收**：`startSessionReclaimer` 用 `app.Go`
  把定期回收挂到 Bot 的生命周期上（此前 `Manager.StartReclaimer` 在组合根从未被调用，
  等于定期回收在生产里不存在）。验收测试见 §2 表格。
  "固化记忆"不需要额外钩子：记忆在写入时已落 SQLite（F-87），回收没有待刷的数据。
- ~~**F-65 提示词前缀稳定化**~~ **已完成**（21059e7）。

范围内的功能全部完成。剩余工作只有 §3.2 的接线；
F-07（多账号）与 F-27（多供应商）明确在目标范围之外。

### 3.2 接线（剩 6 处，按建议顺序）

~~#1 admin 命令入口~~、~~#2 cost 会话维度~~、~~#3 agent.paradigm~~、~~#10 F-82 剩余~~
均已完成，不再列出。

| # | 项 | 入口 | 前置条件 / 风险 |
|---|---|---|---|
| 3 | ~~agent.paradigm（F-36/F-37）~~ **已完成** | — | 已接：LLM Evaluator + wrapParadigm；F-37 目前只有默认单 worker，无 workers 列表配置 |
| 4 | agent.memory 切分层记忆（F-49/F-51） | buildAgent 里 memory 的构造处 | **需要 SQLite TierStore 实现**（internal/memory 只给接口与内存实现）；否则重启丢记忆，是行为退化 |
| 5 | semcache（F-63） | LLM 请求路径 | 需要 Vectorize（embedding 或二值哈希）+ 出口过滤（F-55）。可接在 observedLLM 层 |
| 6 | tree 摘要树（F-52） | 摄入路径 | 需要注入 Summarizer / Embedder |
| 7 | stream -> outbound（F-64） | 发送路径 | llm.NewStreamSplitter{Flush: outbound.NewStreamSender(...).Handler(ctx)}；不要改动请求侧（前缀缓存） |
| 8 | trace（F-72） | 入站上下文 + 出站 HTTP 头 | internal/trace 是自包含 traceparent 子集（**无 OTLP**）。serve 已有 observe.WithTraceID 机制，可桥接 |
| 9 | reload 其余资产（F-24） | 提示词/开关/限速 | 目前只接了敏感词表；提示词热加载要重建 Assembler，需评估前缀缓存语义 |

## 4. 已知偏离与诚实标注（不要当成"已完成"）

1. **F-72 无 OTLP 导出**：OpenTelemetry 不在依赖白名单，只实现了 traceparent 传播/采样/context。完整 OTel 需先走依赖准入。
2. **F-46 是策略层，不是 OS 级隔离**：没有 os/exec 独立进程、cgroup/Job Object、进程组强杀。接口（SandboxDeclarer）已留好。
3. **F-31/F-32 两处签名偏离规格**：JSONSchemaOf[T]() 返回 (ResponseFormat, error)；Fit/Count 带 ctx。理由已写入代码注释。
4. **F-66 的 `downgrade` 动作未强制**：按请求切模型需要 `llm.ChatRequest` 带 model 字段（协议形状改动）。当前会如实告警并继续用原模型。
   会话/用户维度的计量与 deny 已生效，快照落 SQLite。
5. **F-24 只交付敏感词表热加载**（组合根里唯一真正读文件的运行时资产）。
6. **F-63 自定义 Similarity 时退化为有界线性扫描**（包注释已写明理由与上限）。
7. **F-65 的静态段报告只覆盖 system 正文**：工具 schema 随请求的 Tools 字段发送，
   顺序由 F-41 的注册顺序固定，不在 `/prompt-hash` 的 static 段里。
   另：`DefaultPersona` 不注入半静态正文（内置 default.yml 与 `DefaultSystemPrompt`
   逐字相同，再注入一次等于每个请求发两遍同一段话）。

## 5. 工作方式约定（务必遵守）

- **本地跑不了 golangci-lint 与 -race**（无 cgo/gcc）；两者由 CI 把关。这意味着
  "本地全绿"不等于能过 CI——已多次在 CI 才暴露（-race 下的预算断言、
  粗粒度时间戳导致的指纹漏检、queueWriter 关停竞争）。
- 每轮收尾流程：go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l cmd internal
  -> 通过才提交 -> git push origin main -> gh run watch <id> --exit-status。
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
