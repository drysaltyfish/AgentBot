package store

// messagesSchema 是 F-84 的表与索引。
//
// 全部语句幂等（IF NOT EXISTS），因此可以无条件重复执行——这正是"声明式 schema"
// 的前提：scheme 演进时只改这里，不需要写"如果旧版本则…"的分支。
var messagesSchema = []string{
	// 消息本体。
	//   seq     会话内单调递增，是"顺序"的唯一依据（时间戳会撞、会回拨）
	//   kind    区分对话轮次与内部条目（marker），与 history.Kind 对应
	//   fingerprint 幂等导入用：同样的内容重放不产生新行
	`CREATE TABLE IF NOT EXISTS messages (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT    NOT NULL,
		seq         INTEGER NOT NULL,
		role        TEXT    NOT NULL,
		kind        TEXT    NOT NULL,
		content     TEXT    NOT NULL,
		name        TEXT    NOT NULL DEFAULT '',
		tool_call_id TEXT   NOT NULL DEFAULT '',
		tool_calls  TEXT    NOT NULL DEFAULT '',
		token_count INTEGER NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL,
		fingerprint TEXT    NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq
		ON messages(session_key, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_session_created
		ON messages(session_key, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_fingerprint
		ON messages(session_key, fingerprint)`,

	// 全文索引。用 external content 避免正文存两份；rowid 对齐 messages.id。
	//
	// 分词器用 trigram：它按 3 字符滑窗建索引，因此**支持子串检索**，这对中文是必需的
	// ——unicode61 会把整段中文当成一个 token，"橙汁"永远搜不到"我喜欢喝橙汁"。
	// 代价是 trigram 对**短于 3 字符**的查询不生效，所以查询侧必须回退 LIKE（见 search.go）。
	`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		content='messages',
		content_rowid='id',
		tokenize='trigram'
	)`,

	// 三个触发器保持 FTS 与本体同步。
	`CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
		INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
	END`,
}

var messagesColumns = []columnSpec{
	{Table: "messages", Name: "token_count", DDL: "token_count INTEGER NOT NULL DEFAULT 0"},
	{Table: "messages", Name: "fingerprint", DDL: "fingerprint TEXT NOT NULL DEFAULT ''"},
}
