# 34 · ReAct 循环

- **Feature**: F-35（P0）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 33
- **写域（建议）**: internal/agent/

## 目标

让模型能先想、再调工具、看结果、再想——这是 Agent 的核心循环。

## 交付物

- 循环：`for i := 0; i < MaxIterations; i++` { 调用 LLM（带工具 schema）→ 有 tool_calls 则逐个执行并追加 observation → 否则返回最终答案 }
- 默认 `MaxIterations = 10`；达上限返回明确错误并携带已完成的 Steps
- 每步记录 `Step`（thought/action/observation）与耗时；循环内累计 usage 并汇总返回

## 验收

- **关键契约测试**：用 mock server 构造两轮工具调用会话，断言第二轮请求体中 assistant 消息含 `tool_calls`、tool 消息含匹配的 `tool_call_id`
- 达到 MaxIterations 时返回错误且 Steps 完整
- 工具返回 error 时循环**继续**而非终止
- ctx 取消立即返回

## 边界与易错点

- 工具执行失败必须作为 observation 回灌（`{...}` 形式），不中断循环，让模型自行纠错
- 单步工具超时默认 30s 可配，超时后仍要回灌超时信息；**F-45 的人工审批等待占用独立预算，不计入单步超时**
- 一轮内多工具默认**串行**执行；`ParallelTools(true)` 要求工具声明 `ConcurrencySafe`

## 备注（已定决策）

- 见 ADR-0001：本循环**只消费原生 tool_calls**；F-39 的解析器仅作抢救通道（模型把动作吐在文本里时），原生存在时忽略文本动作并记告警
- DeepSeek 思考模式下**携带 tools 时 reasoning_content 必须回传**且会拼进上下文；历史仍须 append-only，否则破坏前缀缓存


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/agent/react.go`（`ReactAgent`）、`internal/agent/react_test.go`（12 个用例），
另补 `llm.Message.ReasoningContent` 与其线路映射。

**关键契约测试落在序列化后的 JSON 上**，而不是内存结构上：

`Test_F35_ToolProtocolContract` 用真实的 `llm.NewOpenAI` 打到 `httptest` mock server，
捕获**第二轮请求体**的原始字节并断言：

- 存在带 `tool_calls` 的 assistant 消息，且 `tool_call.id` 原样回传（`call_abc`）
- `tool` 消息紧跟其后，且 `tool_call_id` 与上一条调用匹配
- 第二轮仍携带工具 schema（否则模型不知道自己能调什么）

这样断言的原因是：只映射 role/content 而不带这两个字段会被服务端 400 拒绝（反模式 #14），
而这个错误在内存结构上是看不出来的——只有序列化结果才暴露它。

**ADR-0001 的落地**：

- 原生 `tool_calls` 是唯一执行通道；`Protocol=native` 时完全关闭抢救
- `Protocol=auto`（默认）下，`tool_calls` 为空时用 F-39 的解析器抢救文本动作，
  并**必须告警**（`Test_F35_ScavengesActionsFromText` 断言告警非空）
- 原生存在时忽略文本动作并告警（`Test_F35_NativeToolCallsWinOverTextActions`）

**DeepSeek 的硬性要求已落实**：携带 tools 时历史轮次的 `reasoning_content` 必须回传
（`Test_F35_ReasoningContentIsEchoedBackWithTools` 断言 assistant 消息同时带
`ReasoningContent` 与 `ToolCalls`）。

**失败一律降级为 observation，循环不中断**：工具返回 error、未知工具名、单步超时
三种情况都转成回灌文本（并有独立测试）。额外的防御：工具 panic 会被 recover 成失败结果，
不杀死进程。

**并发**：默认串行；`ParallelTools=true` 时**只有同轮全部工具都声明 `ConcurrencySafe`
才并发**，只要有一个不安全就整体退回串行。用峰值并发数断言（声明可并发→峰值 2，
未声明→峰值 1），并断言返回值顺序始终与模型声明的顺序一致。

**验收执行**：

```
go test -count=1 ./internal/agent/    → ok（12 个用例）
go test -count=1 ./...                → 18 个包全绿
golangci-lint run                     → 0 issues
go test -proxy... gofmt/build/vet     → 干净
```

**已知未覆盖（按规格属本 ticket 之外）**：

- **人工审批的等待不计入单步超时**：属于 F-45（ticket 38），本 ticket 只留了单步超时的
  接入点，未实现审批等待的独立预算
- `Step.Type=observation` 目前只在 LLM 调用失败时记录；工具观察结果记在 `StepAction.ToolOutput`，
  与规格列出的三种 Step 类型命名略有出入，M2 后续如需可在不改契约的前提下补一条独立 observation 步骤
