# 34 · ReAct 循环

- **Feature**: F-35（P0）
- **里程碑**: M2
- **Status**: open
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
