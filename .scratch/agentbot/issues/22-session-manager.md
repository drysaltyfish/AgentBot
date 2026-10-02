# 22 · F-21 Session 与 Manager

- **Feature**: F-21（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 14
- **写域（建议）**: internal/session/

## 目标

会话级状态需要归属；用实例隔离代替库内加锁。

## 交付物

- `Session{ID SessionKey, Hist History, Persona, caller, mu, lastSeen, data}`
- `SessionKey{SelfID, GroupID, UserID}` + `String()` 返回 `"selfID:groupID:userID"`
- 策略 `PerGroup`（默认）/`PerUser`/`PerUserInGroup`
- `Manager{mu, sessions, policy, ttl, max}` + `GetOrCreate`（超 max 按 LRU 淘汰）+ 后台 ticker（1 分钟）清理超 TTL（30 分钟）会话

## 验收

- 并发 1000 个不同 key 调 `GetOrCreate`，`-race` 通过，实例数不超过 max
- 超 TTL 后会话被回收，内存不持续增长
- 日志/指标里的 `session_key` 一律用 `SessionKey.String()`

## 备注（已定决策）

- Q21 已定：`SessionKey` 必须含 `SelfID`，记忆 `Scope` 同步加
- 淘汰前先固化需持久状态；回收时注销其名下临时路由（F-15/F-16）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/session/session.go` + 单测（10 项）。`Key.String()` = `self:group:user`；**多账号不串台**（selfID 10001/10002 同群得到不同会话，状态不共享）；三种粒度策略折出的键正确；`WithMax(2)` 下访问过 `ka` 后 `kb` 被淘汰而 `ka`/`kc` 保留（`Evicted()==1`）；TTL 过期回收 1 个；回收前先执行 hook 并注销挂在该会话上的临时路由（`RouteRef.Remove` 被调用）；`Close` 关闭 5 个会话并注销 5 条临时路由且幂等；10 goroutine × 100 次并发 `GetOrCreate`（max=50）后实例数不超上限；会话与 `history.Memory` 共享同一实例。
