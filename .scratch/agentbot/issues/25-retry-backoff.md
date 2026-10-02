# 25 · F-30 重试与退避

- **Feature**: F-30（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 23
- **写域（建议）**: internal/llm/

## 目标

上游抖动是常态，业务代码不该为它写循环。

## 交付物

- `RetryPolicy{MaxAttempts, BaseDelay, MaxDelay, Factor, Jitter, Retryable}`；默认 3 次 / 500ms / 30s / ×2 / 带 jitter
- 退避等待可被 ctx 中断；尊重 `Retry-After`
- 实现为 `LLM` 装饰器；流式一旦开始输出就不再重试

## 验收

- 前两次 500、第三次成功 → 最终成功且尝试 3 次
- 返回 400 → 只尝试 1 次
- ctx 在等待期间取消 → 立即返回 `ctx.Err()`
- 每次重试记录指标

## 备注（已定决策）

- 默认可重试：网络错误/超时/429/5xx；不可重试：400/401/403/404
- 面客错误话术不在本 Feature（统一归 F-55）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/llm/retry.go`（装饰器）+ `internal/retry`（通用内核，被 transport 复用）。500 两次后成功 → 共 3 次尝试且返回成功；400 → **只尝试 1 次**（不重复计费）；`DefaultRetryable` 分类表 8 组（500/429/deadline 可重试，400/401/403/404 不可，nil 不可）；退避等待期 ctx 取消 → 立即返回 `context.Canceled`（实测 < 1s）；流式**只在开流前失败才重试**（2 次流尝试后成功输出）。
