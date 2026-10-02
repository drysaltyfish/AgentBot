# 14 · F-11 事件上下文与 State

- **Feature**: F-11（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 11
- **写域（建议）**: internal/router/

## 目标

一次事件处理过程中的共享数据容器，必须每事件独立。

## 交付物

- `Ctx{ctx, bot, Event, State, caller, mu, once, cached}`
- `State map[string]any` + `GetString/GetInt64/GetBool`
- `Ctx` 实现 `context.Context`（Deadline/Done/Err/Value 转发）
- `MessageString()` 用 `sync.Once` 缓存

## 验收

- 两条路由串联：第一条写 `__keep__x`，第二条仍能读到；普通键被清理
- `Ctx.Deadline()` 与内部 ctx 一致
- 并发写 `State` 经 `SetState` 加锁，`-race` 通过

## 备注（已定决策）

- 保留键前缀常量 `StateKeyKeepPrefix = "__keep__"`（已统一为前后各两个下划线）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/ctx.go` + `ctx_test.go`。保留键 `__keep__x` 跨路由存活、普通键与 `StateKeyCommand` 被 `ResetForNextRoute` 清除；`Ctx` 实现 `context.Context`（Deadline/Value/Done/Err 全部转发到内部 ctx）；`MessageString()` 用 `sync.Once` 缓存（改动事件消息后仍返回旧值）；nil Event 安全；20 goroutine × 50 次并发读写 State 无异常；`And/Or/Not/All` 真值表 13 组全过。**偏离说明**：规格里的 `bot *Bot` 字段未实现（避免 router→bot 的反向依赖），需要组合根时经 `Ctx.Value` 传递。
