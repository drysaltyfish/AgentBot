# 46 · 消息归档与全文检索

- **Feature**: F-84
- **里程碑**: M3
- **Status**: resolved
- **Blocked by**: 45
- **写域（建议）**: internal/store/, internal/history/, internal/tool/builtin/

## 目标

把对话历史搬进 @BT@messages@BT@ 表并支持全文检索，让 @BT@recall_history@BT@ 真正能
"从很久以前捞回一条"。

## 交付物

- @BT@messages@BT@ 表 + 索引 @BT@(session_key, seq)@BT@ / @BT@(session_key, created_at)@BT@
- FTS5 虚表 + INSERT/UPDATE/DELETE 触发器；**CJK 友好分词**（trigram 或等价）
- @BT@Search(ctx, sessionKey, query, limit)@BT@：返回片段 + 前后各一条
- 查询清洗：未配对引号、悬空布尔符、连字符词
- @BT@history.History@BT@ 新增 SQLite 实现；JSONL 实现保留用于导入与降级排查
- @BT@recall_history@BT@ 改走 @BT@Search@BT@（**仍只查当前会话**，取不到会话必须失败）

## 验收

- 中文关键词命中，**含子串场景**（不只是整句）
- 跨会话隔离有测试
- 畸形查询（@BT@foo" OR@BT@、@BT@a AND@BT@、@BT@chat-send@BT@）不报错且返回可用结果
- 删除一条消息后 FTS 不再命中它
- @BT@seq@BT@ 并发写入下不跳号、不覆盖

## Comments

### 2026-10-03 · 完成记录

**先探针后实现**。动手前确认了 SQLite 3.53.4 的 FTS5 与 trigram 可用，并实测出两个事实：
trigram 对 **<3 字符**的查询命中为 0；带连字符的查询（`chat-send`）**直接报错**
（`no such column: send`）。前者经用户确认采用**混合方案**，后者印证了查询清洗不是多余的防御。

**交付物**
- `messages` 表 + `(session_key, seq)` 唯一索引 + `(session_key, created_at)` 索引
- `messages_fts`：external content（正文不存两份）+ trigram 分词器 + 三个同步触发器
- `store.Message` 读写 API：`AppendMessage` / `Messages` / `MessageCount` /
  `TotalMessageCount` / `DeleteMessage` / `ResetMessages` / `TrimMessages`
- `SearchMessages`：**混合检索**——≥3 字符走 FTS5 拿片段，更短走 LIKE，
  FTS 未命中或报错再兜底一次 LIKE
- `history.SQLite`：`history.History` 的持久层实现，另实现 `history.Searcher`
- `history.SQLite.ImportJSONL`：**幂等**导入
- `builtin` 新增可选接口 `HistorySearcher`；`recall_history` 优先走检索，
  渲染时带片段与前后文

**踩到并修掉的真 bug（值得记）**：FTS 查询在 `messages_fts JOIN messages` 上用了未限定的
`content`，两边都有该列 → `ambiguous column name: content`。而我写的**静默回退**把它吞了：
表面上"还能搜到"（走了 LIKE），实际 FTS 一次都没成功过。
修复方式有两条，都做了：
1. 用带表名前缀的列清单（`messageColsQ`）
2. **给 Store 加 `Warn` 钩子**，FTS 失败回退时留下痕迹。
降级可以发生，但静默降级是这类问题最难查的形态——这一条写进了代码注释。

**中文检索的取舍（已实测覆盖）**
- `喝橙汁`（3 字）→ FTS，片段 `我喜欢[喝橙汁]`
- `橙汁`/`天气`/`作业`（2 字）→ LIKE 回退，**能命中**（trigram 单独做不到）
- `chat-send`、`foo" OR`、`a AND`、`100%` 等畸形查询不报错

**验收执行**（`go test -count=1 ./internal/store/ ./internal/history/ ./internal/tool/builtin/` 全绿）
- seq 连续分配；带显式 seq 的重复导入全部跳过
- 检索命中片段与前后邻居；只返回本会话
- 删除后 FTS 不再命中
- **导入幂等**：用 `File` 实现写出**真实**的 JSONL 再导入，两次结果一致；
  且按位置分配序号，**合法的重复内容不会被吃掉**（用"第一条"出现两次验证）
- 冒烟：真实 `history.jsonl` 导入 42 条；重启后跳过导入（库中已有 42 条）

**未做**：F-84 提到的"工具轮次成对删除"约束未实现按单条删除的配对维护——
目前 `DeleteMessage` 是原样删除，配对完整性由调用方保证。已在表结构上保留
`tool_call_id` 与 `tool_calls` 两列，后续需要时再补约束。
