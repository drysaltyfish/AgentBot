# 05 · F-67 结构化日志与追踪

- **Feature**: F-67（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 04
- **写域（建议）**: internal/observe/

## 目标

能把一次事件的全过程串起来，且高并发下日志不成为瓶颈。

## 交付物

- 用 `log/slog`；生产 JSON、开发文本
- 每条日志带 `trace_id`、`session_key`、`user_id`、`group_id`、`component`
- 组件级日志级别覆盖；异步写入 + 有界队列 + 丢弃计数

## 验收

- 一次事件产生的所有日志共享同一 trace_id
- 关闭 `debug_content` 时日志中不出现用户消息全文
- 队列满时主流程不受影响且有丢弃计数
- panic 恢复时记录 `debug.Stack()`

## 备注（已定决策）

- `Redactor`（F-61）在 M3 落地；M0 先留注入点
- trace_id 与 OTel（F-72）的关系在 M4 处理

## Comments

### 2026-10-02 · 完成记录

实测：`internal/observe` + 单测 7 项全过；同一 ctx 的 trace_id 贯穿多组件；`debug_content=false` 时全文不落日志；组件级级别可下调根级别；队列满丢弃计数；Redactor 生效；`Recover` 把 panic 转 error 并记堆栈。
