# 23 · F-26 统一 LLM 接口

- **Feature**: F-26（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/llm/

## 目标

上层业务不应知道背后是 OpenAI、Claude 还是本地 Ollama。

## 交付物

- `LLM` 接口：`Chat(ctx, *ChatRequest) (*ChatResponse, error)`、`ChatStream(ctx, *ChatRequest) (<-chan Chunk, error)`
- `ChatRequest`/`ChatResponse` 结构（含 `Tools`、`Temperature`、`MaxTokens`、`ResponseFormat`、`Usage`）
- OpenAI 兼容实现 + `FakeLLM`（脚本化响应序列 + 调用记录）

## 验收

- 用 FakeLLM 替换后，Agent 全链路测试无需网络
- `var _ LLM = (*OpenAI)(nil)` 编译期断言存在
- 空 Messages 返回明确错误

## 备注（已定决策）

- **所有上层结构体字段必须是 `LLM` 接口类型**
- `ChatStream` 必须在 ctx 取消时关闭 channel，且只关闭一次

## Comments

### 2026-10-02 · 完成记录

实测：`internal/llm/llm.go` + `fake.go` + `openai.go` + 单测。`FakeLLM` 按脚本依次应答并记录全部请求；空 Messages 返回 `ErrNoMessages` 且**在发起网络请求之前**就被拒（用 hit 标志断言）；`LLM` 接口断言覆盖 fake 与重试装饰器。**关键契约测试**：抓取真实请求体，断言第 3 条 assistant 消息带 `tool_calls[0].id="call_1"`、`type="function"`、`arguments` 原样，第 4 条 tool 消息带 `tool_call_id="call_1"`，`tools[0].type="function"` —— 这正是反模式 #14 的防线；响应解析含 content/finish_reason/tool_calls/usage；401 → `*StatusError` 且 `DefaultRetryable==false`；端点路径为 `/chat/completions`。
