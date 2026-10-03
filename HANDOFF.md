# 接手文档：FEATURES 剩余工作

> 用途：本会话上下文接近上限时留下的准确状态。**不是完成报告**——
> 目标"除多账号/多供应商路由外全部完成"尚未达成。

## 1. 目标与范围

- 目标（goal）：除 **F-07 多账号路由** 与 **F-27 多供应商路由** 外，FEATURES.md 中其余 Feature 全部完成。
- 规格唯一来源：FEATURES.md（89 条）。
- 仓库：Go 1.27.1，单二进制 + 内嵌 SQLite（modernc），OneBot v11 QQ 机器人。

## 2. 当前状态（干净、已推送）

- HEAD = 1572003（外加本提交），与 origin/main 一致，工作区干净，CI 绿。
- **功能：85/89 完成**；剩 F-23、F-65。
- **接线：5/15 完成**。这是最大的缺口：按仓库自己的 **F-79**，未接线 = 未交付。

### 已完成的接线（5 处）

| Feature | 接线内容 | 证明它的测试 |
|---|---|---|
| F-57 / F-58 | UsePre 挂入站审查；拦截进审计与 guard_blocks 指标 | cmd/server 相关测试 |
| F-82（半） | prompt.personas_dir + 启动期 scoped.Load/ValidateRefs | 目录不存在时不失败（已确认 persona.go 语义） |
| F-24（切片） | 敏感词表热加载（watch -> 重编译 -> SwapMatcher） | Test_F24_SensitiveWordsHotReload（端到端证明"生效"） |
| F-46 | sandbox 分节 + registry.Sandbox（装配最后一步，保序） | Test_F46_SandboxPolicyFromConfig（映射 + 启动期校验） |
| F-66（半） | cost 分节 + observedLLM 按真实 usage 记账 + `/cost` 命令 | Test_F66_ObservedLLMRecordsCost、Test_F66_AdminCostCommandReportsUsage |
| F-71 | admin 模块（/help、/ban、/unban、/banlist）+ 超管鉴权 + 审计 | Test_F71_AdminModuleAuthorizesAndAudits |
| F-36 / F-37 | LLM Evaluator + `agent.paradigm`（reflexion / orchestrator） | Test_F36_ReflexionParadigmDrivesEvaluation（断言评估器被调用 2 次） |

## 3. 剩余工作

### 3.1 功能（2 条）

- **F-23 会话生命周期与回收**（FEATURES.md:713，属"验收"）。
- **F-65 提示词前缀稳定化**（FEATURES.md:1876，属"验收"）。

建议做法：这两条不要各补一个单点测试，而应做**端到端验收**——
多轮工具调用、人格切换后 ComparePrefix 的类别变化、会话回收后的内存/历史释放。
internal/textsim 已有 ComparePrefix（RelationSlid / Diverged 等），
F-82 的 RouteKey / Fingerprint 是 F-65 的输入。

### 3.2 接线（10 处，按建议顺序）

| # | 项 | 入口 | 前置条件 / 风险 |
|---|---|---|---|
| 1 | admin 命令入口 | cmd/server/serve.go 的消息路径 | 包已就绪（internal/admin）。同时挂 /switch（toggle）与 /ban 系列（internal/moderation 的 Commander）。Authorizer 复用 moderation.super_users |
| 2 | cost 会话维度 + 持久化 | 会话键需进 LLM 调用链 | 当前只做全局计量；要强制 session/user 配额，必须把会话键经 ctx 传到 observedLLM，再实现 SQLite cost.Store |
| 3 | agent.paradigm（F-36/F-37） | cmd/server/build.go 的 buildAgent | **必须先写 LLM 驱动的 Evaluator**：ReflexionAgent 在 Evaluator==nil 时只是原样返回初稿（reflexion.go:98），接了等于空转 |
| 4 | agent.memory 切分层记忆（F-49/F-51） | buildAgent 里 memory 的构造处 | **需要 SQLite TierStore 实现**（internal/memory 只给接口与内存实现）；否则重启丢记忆，是行为退化 |
| 5 | semcache（F-63） | LLM 请求路径 | 需要 Vectorize（embedding 或二值哈希）+ 出口过滤（F-55）。可接在 observedLLM 层 |
| 6 | tree 摘要树（F-52） | 摄入路径 | 需要注入 Summarizer / Embedder |
| 7 | stream -> outbound（F-64） | 发送路径 | llm.NewStreamSplitter{Flush: outbound.NewStreamSender(...).Handler(ctx)}；不要改动请求侧（前缀缓存） |
| 8 | trace（F-72） | 入站上下文 + 出站 HTTP 头 | internal/trace 是自包含 traceparent 子集（**无 OTLP**）。serve 已有 observe.WithTraceID 机制，可桥接 |
| 9 | reload 其余资产（F-24） | 提示词/开关/限速 | 目前只接了敏感词表；提示词热加载要重建 Assembler，需评估前缀缓存语义 |
| 10 | F-82 剩余 | 会话人格持久化 + RouteKey 喂半静态段 | 实现 scoped.PersonaStore（SQLite），把 Manager.RouteKey/Fingerprint 接进半静态段（F-65 前置） |

## 4. 已知偏离与诚实标注（不要当成"已完成"）

1. **F-72 无 OTLP 导出**：OpenTelemetry 不在依赖白名单，只实现了 traceparent 传播/采样/context。完整 OTel 需先走依赖准入。
2. **F-46 是策略层，不是 OS 级隔离**：没有 os/exec 独立进程、cgroup/Job Object、进程组强杀。接口（SandboxDeclarer）已留好。
3. **F-31/F-32 两处签名偏离规格**：JSONSchemaOf[T]() 返回 (ResponseFormat, error)；Fit/Count 带 ctx。理由已写入代码注释。
4. **F-66 仅全局计量**，session/user 配额未生效；持久化走内存。
5. **F-24 只交付敏感词表热加载**（组合根里唯一真正读文件的运行时资产）。
6. **F-63 自定义 Similarity 时退化为有界线性扫描**（包注释已写明理由与上限）。

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
