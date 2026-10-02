# 16 · F-09 稳定优先级排序

- **Feature**: F-09（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 15
- **写域（建议）**: internal/router/

## 目标

同优先级路由的执行顺序必须可预测。

## 交付物

- 按 `Priority` 升序，同优先级保持注册顺序（`sort.SliceStable`）
- 增删/改优先级后重排一次并递增 `epoch`
- 语义常量 `PriorityFirst=0`/`Early=10`/`Normal=50`/`Late=90`/`Last=100`

## 验收

- 注册 A(50)、B(10)、C(50)，执行顺序为 B → A → C
- 反复增删后相同集合顺序稳定
- 空路由列表不 panic

## 备注（已定决策）

- 排序必须在锁内完成；`epoch` 供匹配侧判断快照过期

## Comments

### 2026-10-02 · 完成记录

实测：注册 A(50)/B(10)/C(50) 的执行顺序为 B→A→C（`Routes()` 顺序断言）；30 条同优先级路由删掉一半后剩余仍严格保持注册顺序；空路由表下 `Len/Routes/Snapshot` 均安全。
