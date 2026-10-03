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

var sessionsSchema = []string{
	// 会话台账（F-85）。
	//
	// 计数一律是**累加列**，靠 upsert 的 "列 = 列 + 增量" 维护。
	// 不用"读改写"是因为并发下会丢增量，而丢增量不会有任何报错——
	// 只是数字慢慢变得不可信。
	`CREATE TABLE IF NOT EXISTS sessions (
		session_key        TEXT PRIMARY KEY,
		first_seen         INTEGER NOT NULL,
		last_active        INTEGER NOT NULL,
		requests           INTEGER NOT NULL DEFAULT 0,
		tool_calls         INTEGER NOT NULL DEFAULT 0,
		input_tokens       INTEGER NOT NULL DEFAULT 0,
		output_tokens      INTEGER NOT NULL DEFAULT 0,
		cache_hit_tokens   INTEGER NOT NULL DEFAULT 0,
		cache_miss_tokens  INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
		estimated_cost_usd REAL    NOT NULL DEFAULT 0,
		pricing_version    TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_last_active ON sessions(last_active DESC)`,
}

var sessionsColumns = []columnSpec{
	{Table: "sessions", Name: "pricing_version", DDL: "pricing_version TEXT NOT NULL DEFAULT ''"},
	{Table: "sessions", Name: "estimated_cost_usd", DDL: "estimated_cost_usd REAL NOT NULL DEFAULT 0"},
}

var memoriesSchema = []string{
	// 长期记忆（F-87）。
	//
	// scope_key 参与**所有**读写路径：跨作用域串读是隐私缺陷，不是粒度选择。
	// fingerprint 是"作用域 + 正文"的指纹，让重放天然幂等。
	`CREATE TABLE IF NOT EXISTS memories (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		scope_key   TEXT    NOT NULL,
		kind        TEXT    NOT NULL DEFAULT 'fact',
		title       TEXT    NOT NULL DEFAULT '',
		text        TEXT    NOT NULL,
		source_refs TEXT    NOT NULL DEFAULT '[]',
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		score       REAL    NOT NULL DEFAULT 0,
		fingerprint TEXT    NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_scope_fingerprint
		ON memories(scope_key, fingerprint)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope_key, id)`,
}

var memoriesColumns = []columnSpec{
	{Table: "memories", Name: "score", DDL: "score REAL NOT NULL DEFAULT 0"},
	{Table: "memories", Name: "title", DDL: "title TEXT NOT NULL DEFAULT ''"},
	{Table: "memories", Name: "source_refs", DDL: "source_refs TEXT NOT NULL DEFAULT '[]'"},
}

var pendingSchema = []string{
	// 在途操作（F-86）：等待下一条消息、等待人工审批。
	//
	// 存在这张表里的理由是"重启不该让用户悬在空中"：一次审批可能已经问过人了，
	// 确认回来时进程却已经忘了在等什么。
	//
	// 记录**不随完成而删除**，只改状态：谁在什么时候批准/拒绝/超时，正是审计要回答的。
	// 表的有界性由 PrunePending 保证。
	`CREATE TABLE IF NOT EXISTS pending (
		id          TEXT PRIMARY KEY,
		session_key TEXT    NOT NULL,
		kind        TEXT    NOT NULL,
		payload     TEXT    NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL DEFAULT 0,
		status      TEXT    NOT NULL,
		note        TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_pending_status ON pending(status, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_pending_session ON pending(session_key)`,
}

var promptSnapshotSchema = []string{
	// 提示词快照（F-89）。
	//
	// 存的是**消息指纹**而不是正文：正文可能含隐私，而前缀比较只需要判断
	// "这一段是否逐字节相同"。逐条指纹还让"分歧发生在哪一条"可直接算出。
	//
	// relation 落库是必要的：只记录指纹而不记录"和上一条比怎么样了"，
	// 事后就无法回答"前缀是从哪一轮开始不稳的"。
	`CREATE TABLE IF NOT EXISTS prompt_snapshots (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key   TEXT    NOT NULL,
		seq           INTEGER NOT NULL,
		digest        TEXT    NOT NULL,
		message_count INTEGER NOT NULL DEFAULT 0,
		relation      TEXT    NOT NULL DEFAULT '',
		common_prefix INTEGER NOT NULL DEFAULT 0,
		slid_by       INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_snapshots_session_seq
		ON prompt_snapshots(session_key, seq)`,
}
