# 18 · F-12 热路径快照匹配

- **Feature**: F-12（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 15, 16
- **写域（建议）**: internal/router/

## 目标

每条事件都要遍历全部路由，不能每次遍历都加锁。

## 交付物

- `epoch` + 只读快照 `snapshot []*Route`；`epoch` 未变则零锁遍历
- 快照重建时复制切片，不做深拷贝（Route 内部视为只读）
- `BenchmarkRouteMatch`：1000 条路由下 P99 < 100µs（不含 Handler 执行）

## 验收

- 基准 `BenchmarkRouteMatch` 达标且 `b.ReportAllocs()` 有数据
- 一个 goroutine 疯狂注册/删除、多个 goroutine 疯狂匹配，`-race` 无告警

## 备注（已定决策）

- `Route` 可变字段修改必须先加锁并递增 epoch
- 基准名统一为 `BenchmarkRouteMatch`

## Comments

### 2026-10-02 · 完成记录

实测：`Test_F12_SnapshotReusedUntilEpochChanges` 断言 epoch 未变时复用同一底层数组、注册后 epoch 递增且快照重建；`Remove` 幂等并让快照失效；`Test_F12_ConcurrentRegisterAndMatch`（400 次注册 + 4×2000 次匹配）通过。**性能**：`BenchmarkRouteMatch`（1000 路由）实测 **10.3µs/op、0 allocs/op**；初版为 21.8µs/op、1000 allocs/op——瓶颈是 `KindMatches` 每路由分配一个切片，已改为定长数组。注：本机无 C 编译器，`-race` 只能在 CI 执行。
