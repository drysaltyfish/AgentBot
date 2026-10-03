# 46 · 消息归档与全文检索

- **Feature**: F-84
- **里程碑**: M3
- **Status**: open
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
