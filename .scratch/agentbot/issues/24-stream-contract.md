# 24 · F-28 流式契约

- **Feature**: F-28（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 23
- **写域（建议）**: internal/llm/

## 目标

流式让用户感知更快，但它最容易泄漏 goroutine。

## 交付物

- `Chunk{Content, ToolCalls, FinishReason, Done, Err}`；错误也走 channel
- 生产者负责 `close(ch)`；channel 带缓冲（默认 16）
- 所有发送必须 `select { case ch <- c: case <-ctx.Done(): return }`

## 验收

- 消费者读 3 个 chunk 后 break + cancel，生产者 goroutine 在 100ms 内退出（goleak 验证）
- 上游错误时最后一个 chunk 的 `Err` 非空且 `Done=true`
- 上游 EOF 与错误都转成终止 chunk 后关闭

## 备注（已定决策）

- 禁止向已关闭的 channel 发送；消费侧应 `defer cancel()`
- 本 ticket 的契约测试是 M1 完成判据之一

## Comments

### 2026-10-02 · 完成记录

实测：`Chunk` 携带 `Err`/`Done`，生产者负责 close，所有发送经 `SendChunk`（监听 ctx）。`TestMain` 用 `goleak.VerifyTestMain` 守护。验收对应项：50 分片流消费者读 3 个后 cancel，**生产者 200ms 内退出且 channel 关闭**；上游错误转成最后一个 `Err!=nil && Done==true` 的终止分片；ctx 取消后流终止；SSE 解析得到 `hello` + 终止分片 + `finish_reason=stop`；上游 502 在开流前同步返回 `*StatusError`。
