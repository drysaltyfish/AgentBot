# 27 · F-34 统一 Agent 契约

- **Feature**: F-34（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 23, 26
- **写域（建议）**: internal/agent/

## 目标

不同范式共用同一出口，IM 层不必为每种范式写分支。

## 交付物

- `Agent` 接口 `Run(ctx, Input) (*Output, error)`
- `Input{Query, History, SessionKey SessionKey, Files}`
- `Output{Text, Steps, Usage, ToolCalls, FinishReason}`；`Step{Type, Content, ToolName, ToolInput, ToolOutput, DurationMS, Error}`
- 一个最小实现（直答）+ FakeLLM 驱动的端到端测试

## 验收

- 两种 Agent 实现互换，上层 Handler 代码零修改
- `Run` 在 ctx 取消时尽快返回
- `Steps` 在任何退出路径上都填充完整
- `Output.Text` 为空但 `ToolCalls` 非空是合法结果

## 备注（已定决策）

- **不设** `Metadata map[string]any`
- `ToolCalls` 是"本轮**已执行**的调用记录"，不是待执行队列

## Comments

### 2026-10-02 · 完成记录

实测：`internal/agent/agent.go` + 单测。**两个实现可互换**：一个只依赖 `Agent` 接口的 handler 分别驱动 `DirectAgent` 与测试 stub，代码零修改；system 提示词前置、query 后置；nil `DirectAgent` 与未配置 LLM 都返回 `ErrNoLLM` 且 **Output 非 nil、不 panic**；错误路径上 `Steps` 仍被填充；`Text==""` 但 `ToolCalls` 非空是合法结果；ctx 取消后 1s 内返回。按规格**不设** `Metadata` 字段。
