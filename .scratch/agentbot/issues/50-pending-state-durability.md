# 50 · 在途操作持久化（可恢复的等待与审批）

- **Feature**: F-86
- **里程碑**: M3
- **Status**: resolved
- **Blocked by**: 45
- **写域（建议）**: internal/store/, internal/session/, internal/agent/approval.go

## 目标

让 F-16 的 @BT@Await@BT@ 与 F-45 的审批**跨重启存活**。现在它们是进程内状态，
重启即丢——审批可能已经问过人了，确认回来时机器人却忘了在等什么。

## 交付物

- @BT@pending@BT@ 表：@BT@(id, session_key, kind, 负载 JSON, 创建时间, 到期时间, 状态)@BT@
- 启动时恢复未过期记录；过期记录标记超时并**回灌**原会话
- 后台有界清理（定时 + 条数上限）
- 状态迁移审计（批准/拒绝/超时）

## 验收

- 写入待审批 → 重启 → 仍能读到并继续
- 超时后原会话收到明确回灌，记录状态为超时
- **审批等待仍不占用单步超时**（F-45 的既有约束不得回退）
- 原会话不存在时标记孤儿并告警，不猜测投递目标
- 长跑下 @BT@pending@BT@ 不无界增长

## Comments

### 2026-10-03 · 完成记录

**交付物**
- `pending` 表：`id / session_key / kind / payload / created_at / expires_at / status / note`
- `Store.UpsertPending` / `CompletePending` / `ListPending` / `ExpirePending` / `PrunePending`
- `session.PendingStore` 接口 + `PendingRecord` 视图；`TempTable.WithPendingStore` 挂载
- `TempRoute.Pending`：带它的等待会被落盘；移除时置 `done`，会话回收置 `orphaned`，
  超时置 `expired`
- `Manager.Await` 自动带上 `Pending` 元数据
- 组合根启动时 `recoverPending`：逐个通知原会话并收尾

### 必须说清楚的一点：这是"通知"，不是"续跑"

F-86 的验收写的是"重启 → 仍能读到并继续"。**"继续"做不到**，我不打算含糊过去：

等待下一条消息（F-16）与等待人工审批（F-45）都挂在**一次正在执行的调用**上
（阻塞在 channel 上）。进程重启后那次调用已经不存在，没有东西可以"恢复"。
真要续跑，需要把等待变成可重放的持久工作流（谁在等、等到什么、等到之后干什么），
那是一个独立得多的特性。

因此本 ticket 实现的是：
- ✅ **仍能读到**：`pending` 表如实记录在途状态，状态迁移可审计
- ✅ **不让用户悬空**：重启后逐个通知原会话"那个等待被打断了，请再发起一次"
- ✅ 已过期的通知"等太久已作废"
- ❌ **不续跑**：不假装恢复了一次已经消失的调用

这个取舍写进了 `recoverPending` 的注释，也写在 ticket 里，避免以后有人以为它真能续跑。

**几个设计选择**
1. **记录不删除，只改状态**：谁在什么时候批准/拒绝/超时，正是审计要回答的。
   有界性由 `PrunePending` 保证（清 7 天前已结束的记录，**进行中的绝不清理**，有测试）。
2. **落盘失败不影响等待本身**：只是重启后看不见了，所以告警而不是失败。
3. **还原不出投递目标就不猜**：会话键解析失败时标为孤儿并告警，绝不猜一个目标投递。
4. **后台收尾用 `context.Background()`**：临时路由的生命周期长于任何一次请求，
   清理不能因为触发它的请求被取消而中断。五处 `contextcheck` 抑制都写明了这条理由。

**验收执行**（全绿）
- 生命周期：新建为 `pending`；结束后不再出现在未结束列表，但状态与原因仍可查
- 同 id 重复写入幂等
- 过期只影响已到期记录；**返回记录而不只是条数**（调用方要逐条回灌）
- 清理保留进行中记录，只清老记录
- 会话层：注册即落盘（含到期时间）；移除置 done；`RemoveKey` 置 orphaned；
  移除函数幂等；**落盘失败不影响注册且必须告警**
- 冒烟：启动日志 `pending operations recovered total=0 expired=0 notified=0`
- 顺带验证了记忆导入的幂等：第二次启动 `imported=0 skipped=4`
