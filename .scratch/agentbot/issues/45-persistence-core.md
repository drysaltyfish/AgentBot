# 45 · 嵌入式持久层与版本化迁移

- **Feature**: F-83
- **里程碑**: M3
- **Status**: resolved
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

## Comments

### 2026-10-03 · 完成记录

**探针先行**：动手前先跑了一次能力探针，确认驱动与 FTS5 的真实行为（SQLite 3.53.4、
FTS5 可用、trigram 可用）。探针顺带发现了两个必须在 F-84 处理的事实：
- **trigram 对短于 3 字符的查询命中为 0**（「橙汁」「天气」都搜不到），而中文里两字词最常见
- 带连字符的查询（`chat-send`）会**直接报错**（`no such column: send`）

第二条印证了 F-84"查询必须清洗"不是多余的防御。第一条经与用户确认，F-84 采用
**混合方案**：trigram 为主，短查询回退 LIKE。

**交付物**
- `internal/store`：`Open` / `Write` / `Read` / `Close`
- DSN 带 pragma：WAL、`busy_timeout`、`synchronous=NORMAL`、`foreign_keys=1`、
  **`_txlock=immediate`**（写锁在 BEGIN 时获取，锁竞争在事务开始暴露而不是中途）
- 单写者：`writeMu` 串行化写事务；池子开到 8 条连接让读并发
- 抖动重试：20–150ms + 低抖动，最多 15 次；`sleepCtx` 可被取消（不用 time.Sleep）
- 每 50 次成功写做一次 PASSIVE checkpoint
- 迁移：`schema_version` 单行表 + 版本门控链 + 声明式加列对账（`ALTER TABLE ADD COLUMN`）
- `Fingerprint`：字段间插 0 字节分隔，避免 `("ab","c")` 与 `("a","bc")` 撞同一指纹
- 配置 `store.path` / `store.busy_timeout`；组合根打开失败即**启动失败**

**验收执行**（`go test -count=1 ./internal/store/` 全绿）
- 建库后 `schema_version = 1`；`PRAGMA journal_mode` 确认为 `wal`
- 迁移链**幂等**：第二次打开不重复应用，seed 只插一次
- 迁移**中断可恢复**：第 2 条失败后整条回滚，修好后重跑从断点继续，三张表齐备
- 声明式加列：既有数据保留、默认值正确、第三次打开幂等
- **16 goroutine × 20 次并发读写**：无 `database is locked`，累加无丢失（=320）
- 父路径是文件时 `Open` **明确失败**，不降级
- 关库后写入返回 `ErrClosed`
- 冒烟：启动日志 `persistence store is ready schema_version=1`，WAL 的 `-wal`/`-shm` 文件到位

**已知取舍**：`Store.Write` 的 `fn` 可能被重试重复调用，因此**只能是纯 SQL、不得有外部副作用**。
这写进了函数注释——重试语义必须让调用方知道，否则某天有人在里面发一个 HTTP 请求。

**未做**：JSONL 导入落在 F-84（历史）与 F-87（记忆）里实现，那里才有对应的表。
