# ADR 0004 · F-31 结构化输出：能力保留，暂不接线

- 状态：已接受
- 日期：2026-10-07
- 关联：F-31（结构化输出）、F-32（上下文预算）、F-87（语义判官）、internal/llm/structured.go、cmd/server/build.go
- 取代：无
- 对应 spec §6 登记项

## 背景

F-31 要求实现 provider 原生的结构化输出：`ResponseFormat{Type, Schema, Strict}`、
`JSONSchemaOf[T]()` 反射生成 schema、以及在端点不支持时降级。

**实现是完整的，链路也是通的**：

- `openai.go` 在 `req.ResponseFormat != nil` 时序列化 `response_format`，
  并经 `AdaptResponseFormat` 按端点的 `DisableJSONSchema` 能力降级
  （`json_schema` → `json_object`）；
- `structured.go` 提供 schema 生成、严格校验、`ChatStructured`（解析失败后
  追加一轮"只输出 JSON"的重试）。

但**没有任何生产路径设置 `req.ResponseFormat`**，所以整块能力处于休眠状态。

审计时要判断的是"该不该接线"。唯一自然的消费方是 **F-87 的语义判官**：
它要求模型"只允许回答一个字：是 或 否"，然后用 `memory.ParseVerdict`
做**前缀匹配**解析；解析失败返回 `ErrJudgeUnparsed`，调用方退回确定性相似度。

## 决策

**本轮不接线。保留能力，先把"要不要接"变成可回答的问题。**

理由：

1. **判官是软依赖，且已有安全退化路径。** `sameFact` 在判官报错时退回相似度阈值
   （`internal/memory/tier.go`），写入照常成功。给一个"让判定更准"的可选功能
   加上 provider 能力依赖，方向是反的——某些端点会直接拒绝 `response_format`，
   那会让**今天能用**的部署变成判官报错。
2. **降级档位不提供保证。** `AdaptResponseFormat` 把 `json_schema` 降成
   `json_object`，而后者只保证"是 JSON"，不约束字段与取值——恰恰不解决
   "回答是/否"这个形状问题。真正有效的那一档（`json_schema`）在部分端点上不可用。
3. **它默认关闭。** `agent.memory_judge.enabled` 默认 false，接线后默认部署里
   一行都不会执行。
4. **没有依据判断问题是否存在。** 判官解析失败是**静默退化**：没有指标、没有日志
   计数。"解析失败率是多少"此前根本无从回答，因此这个决定是不该拍的。

## 后果

- F-31 的能力继续保留在库里，并在 HANDOFF「未接线清单」里标记为 A 类。
- **新增指标 `memory_judge_verdicts_total{outcome}`**
  （`same` / `different` / `unparsed` / `error`），由 `cmd/server` 的
  `countingJudge` 装饰器记录。`unparsed` 与 `error` 必然是真实模型调用
  （不会写入判官缓存），因此可以直接当解析失败率读。
- 决策的**反转条件**（满足任一即重新评估）：
  1. 线上观测到 `unparsed` 占比不可忽略——那时结构化输出是修复手段，不是优化；
  2. 出现一个**要求**结构合法输出的消费方（例如把文本协议的动作解析
     迁到结构化输出），此时"失败即不可用"而不是"失败即退化"；
  3. 配置面能声明 `response_format` 支持能力（当前只有端点级的
     `DisableJSONSchema`），使不支持该字段的部署可以显式绕开。

在条件 1 满足之前，接线属于**用新的依赖换取一个尚未观测到的问题**。
