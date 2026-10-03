# 40 · 交互式等待（Await/Stream）

- **Feature**: F-16（P0）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 39
- **写域（建议）**: internal/session/, internal/agent/

## 目标

多轮交互：机器人问一句、等用户回答、再继续。

## 交付物

- `Await(ctx) (event.Event, error)`：挂起当前处理，等待该会话的下一条消息
- 基于 F-15 的临时路由实现；ctx 取消或超时返回明确错误
- 多个并发 Await 互不串扰（按会话隔离）

## 验收

- Await 能收到下一条同会话消息并返回
- 超时与 ctx 取消都返回可判别错误，且**不泄漏临时路由**
- 两个会话各自 Await 时不会互相收到对方的消息

## 边界与易错点

- Await 期间不得阻塞事件读循环（否则整个机器人卡死）
- Await 必须计入 F-70 的在途工作，优雅关闭时能正确等待/超时

## 备注（已定决策）

- M2 的完成判据之一即"Await 多轮对话可用"


## Comments

### 2026-10-03 · 完成记录

**交付物**：`Manager.Await`（`internal/session/session.go`）+ 4 个验收用例。

实现方式：注册一条 `Once` + `TTL` 的临时路由，配一个**带缓冲的 channel**，
`Deliver` 用非阻塞投递。因此：

- **不阻塞事件读循环**：读循环只是投递，永不等待等待方来取
- **不泄漏**：`defer remove()` 保证 ctx 取消后立刻摘掉路由；测试断言取消后
  `TempTable` 为空，且后续消息**不会**被一个已经离开的等待者吞掉
- **按会话隔离**：两个会话各自 Await 时互不串扰（测试分别投递、分别断言）
- `match` 为 nil 会被拒绝（`ErrAwaitNoMatch`）——否则一条 Await 会吞掉该会话全部消息

**验收执行**：`go test -count=1 ./internal/session/` → ok；全包绿；lint 0 issues。

**已知边界**：`Await` 目前只支持"下一条消息"；F-16 标题里的 Stream（连续多条）未实现——
规格的验收点只要求"Await 能收到下一条同会话消息并返回"，多轮由调用方循环调用 Await 即可。
若后续要做真正的 Stream，可在同一 `TempTable` 上加非 Once 的路由并把 channel 交给调用方。
