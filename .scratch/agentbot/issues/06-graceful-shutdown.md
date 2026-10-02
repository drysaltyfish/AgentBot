# 06 · F-70 优雅关闭（M0：骨架）

- **Feature**: F-70（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 04
- **写域（建议）**: internal/observe/, cmd/server/

## 目标

先把关闭的骨架与顺序定死，后续里程碑往里填组件；M0 只需让"空 Bot"能优雅关闭且无 goroutine 泄漏。

## 交付物

- `signal.NotifyContext(SIGINT, SIGTERM)` 触发 `Bot.Shutdown(ctx)`
- 6 步关闭顺序：停收新事件 → 等在途（10s）→ 停后台任务 → 关会话 → 关 Driver → flush 并关存储
- 后台 goroutine 全注册到 WaitGroup；Shutdown 幂等；二次 SIGTERM 立即强退

## 验收

- 空 Bot 的 Shutdown 在超时内返回，`goleak` 无发现
- 连续两次 Shutdown 不 panic
- 顺序与超时的唯一定义在本 Feature（F-23 只接入"关会话"一步）

## 备注（已定决策）

- goleak 是唯一 M0 测试依赖（`go.uber.org/goleak`, MIT）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/bot` + 单测 6 项全过，`goleak.VerifyTestMain` 无发现；关闭顺序 intake→background→session→transport→storage；幂等；超时会报未完成组件并 wrap `ErrShutdownTimeout`。
