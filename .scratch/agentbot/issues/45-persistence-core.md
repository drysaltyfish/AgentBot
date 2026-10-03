# 45 · 嵌入式持久层与版本化迁移

- **Feature**: F-83
- **里程碑**: M3
- **Status**: open
- **Blocked by**: 无
- **写域（建议）**: internal/store/（新包）, internal/config/, cmd/server/

## 目标

建立唯一持久层：打开 SQLite、WAL、写竞争处理、迁移链、JSONL 导入。
这是一切的前提，必须最先落地且**先于**任何功能搬移。

## 交付物

- @BT@internal/store/@BT@：@BT@Open(path)@BT@ 返回句柄；WAL、@BT@busy_timeout=1s@BT@、
  @BT@BEGIN IMMEDIATE@BT@、抖动重试（20–150ms，最多 15 次）、每 50 次写做 PASSIVE checkpoint
- @BT@migrate@BT@：@BT@schema_version@BT@ 单行表 + 版本门控链；纯加列走声明式对账（幂等）
- 一次性导入：@BT@agentbot --import-jsonl <path>@BT@，按内容指纹幂等去重
- 配置：@BT@store.path@BT@（默认 @BT@./data/agentbot.db@BT@）
- 打不开即**启动失败**，并给出可读原因

## 验收

- 空库迁到当前版本，@BT@schema_version@BT@ 正确
- 迁移中断后重跑，结果与一次跑完一致（构造中断用例）
- 16 个 goroutine 并发读写：@BT@-race@BT@ 干净，无 @BT@database is locked@BT@ 逃逸
- JSONL 导入两次，结果与导入一次相同
- 只读文件系统上启动时明确失败，而不是降级为内存
