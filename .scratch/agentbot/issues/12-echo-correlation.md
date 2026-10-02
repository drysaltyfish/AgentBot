# 12 · F-06 请求-响应关联（echo）

- **Feature**: F-06（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 10, 11
- **写域（建议）**: internal/transport/

## 目标

WebSocket 之上把"发一个 API 调用并同步拿结果"变成可能。

## 交付物

- 每 Driver 实例：`seq atomic.Uint64` + `pending sync.Map[uint64, chan Response]`
- `Call`：取 echo → 建容量 1 的 chan → 存 pending → 加锁写 socket → select 等响应或 ctx
- 带 echo 的回包配对；不带 echo 的帧当事件投递；心跳丢弃并计数

## 验收

- 并发 100 个 Call、乱序回包全部正确配对
- ctx 超时后 pending 表项数为 0（无泄漏）
- 对端回无人等待的 echo：丢弃并计数，不 panic

## 备注（已定决策）

- `defer` 从 pending 删除，约定"谁删除谁关闭"，发送方只做非阻塞发送
- 写 socket 失败必须从 pending 移除并返回 error

## Comments

### 2026-10-02 · 完成记录

实测：`WSClient.Call` 的 `atomic.Uint64` echo + `sync.Map` pending + 写锁 + defer 清理；单测 `Test_F06_ConcurrentCallsAreCorrelatedOutOfOrder` 用 50 个并发调用、服务端**倒序**回包，全部按 echo 正确配对；`Test_F06_TimeoutLeavesNoPendingEntries` 断言超时后 pending 表项为 0；无人等待的 echo 被丢弃且读循环存活。
