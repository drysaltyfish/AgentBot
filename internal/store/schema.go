package store

// tableDef 是「一张表的完整形态」：建表语句、索引、附属语句与声明式维护的列。
//
// 这是 schema 的唯一声明处：schemaSQL 与 desiredColumns 都由 tables 派生，
// 因此不可能出现「schema.go 加了表、migrate.go 忘了列」这种两处各说各话的情况。
// 新增一张表只需在 tables 里加一项。
//
// 版本门控迁移不登记在表定义里：它是一条按版本号排序的全局链（见 migrate.go 的
// migrations），与单张表不是一一对应，塞进表定义反而要重新排序。
type tableDef struct {
	// CreateSQL 是建表语句，必须幂等（IF NOT EXISTS）。
	CreateSQL string
	// Indexes 是该表的索引语句，同样幂等。
	Indexes []string
	// Attached 是索引之外的附属语句：FTS 虚拟表、同步触发器等。
	Attached []string
	// Columns 是声明式维护的列；缺失时由 reconcileColumns 补齐。
	Columns []columnSpec
}

// schemaVersionTableSQL 是元数据表的建表语句。
//
// 它不放进 tables：这张表没有业务列，也必须在 reconcileColumns 之前就存在。
const schemaVersionTableSQL = `CREATE TABLE IF NOT EXISTS schema_version (
	id      INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
)`

// tables 是全部业务表的声明式登记表，顺序即建表顺序。
//
// 每一项的语句都必须幂等（IF NOT EXISTS），因此可以无条件重复执行——
// 这正是「声明式 schema」的前提：演进时只改这里，不需要写「如果旧版本则…」的分支。
var tables = []tableDef{
	{
		// 消息本体。
		//   seq     会话内单调递增，是「顺序」的唯一依据（时间戳会撞、会回拨）
		//   kind    区分对话轮次与内部条目（marker），与 history.Kind 对应
		//   fingerprint 幂等导入用：同样的内容重放不产生新行
		CreateSQL: `CREATE TABLE IF NOT EXISTS messages (
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
			fingerprint TEXT    NOT NULL,
			speaker_id   INTEGER NOT NULL DEFAULT 0,
			speaker_name TEXT    NOT NULL DEFAULT '',
			ambient      INTEGER NOT NULL DEFAULT 0
		)`,
		Indexes: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq
				ON messages(session_key, seq)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_session_created
				ON messages(session_key, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_fingerprint
				ON messages(session_key, fingerprint)`,
		},
		// 全文索引与三个同步触发器。
		//
		// 用 external content 避免正文存两份；rowid 对齐 messages.id。
		// 分词器用 trigram：它按 3 字符滑窗建索引，因此支持子串检索，这对中文是必需的
		// ——unicode61 会把整段中文当成一个 token，「橙汁」永远搜不到「我喜欢喝橙汁」。
		// 代价是 trigram 对短于 3 字符的查询不生效，所以查询侧必须回退 LIKE（见 message.go）。
		Attached: []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
				content,
				content='messages',
				content_rowid='id',
				tokenize='trigram'
			)`,
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
		},
		Columns: []columnSpec{
			{Table: "messages", Name: "token_count", DDL: "token_count INTEGER NOT NULL DEFAULT 0"},
			{Table: "messages", Name: "fingerprint", DDL: "fingerprint TEXT NOT NULL DEFAULT ''"},
			{Table: "messages", Name: "speaker_id", DDL: "speaker_id INTEGER NOT NULL DEFAULT 0"},
			{Table: "messages", Name: "speaker_name", DDL: "speaker_name TEXT NOT NULL DEFAULT ''"},
			{Table: "messages", Name: "ambient", DDL: "ambient INTEGER NOT NULL DEFAULT 0"},
		},
	},
	{
		// 会话台账（F-85）。
		//
		// 计数一律是累加列，靠 upsert 的「列 = 列 + 增量」维护。
		// 不用「读改写」是因为并发下会丢增量，而丢增量不会有任何报错——
		// 只是数字慢慢变得不可信。
		CreateSQL: `CREATE TABLE IF NOT EXISTS sessions (
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
		Indexes: []string{
			`CREATE INDEX IF NOT EXISTS idx_sessions_last_active ON sessions(last_active DESC)`,
		},
		Columns: []columnSpec{
			{Table: "sessions", Name: "pricing_version", DDL: "pricing_version TEXT NOT NULL DEFAULT ''"},
			{Table: "sessions", Name: "estimated_cost_usd", DDL: "estimated_cost_usd REAL NOT NULL DEFAULT 0"},
		},
	},
	{
		// 长期记忆（F-87）。
		//
		// scope_key 参与所有读写路径：跨作用域串读是隐私缺陷，不是粒度选择。
		// fingerprint 是「作用域 + 正文」的指纹，让重放天然幂等。
		CreateSQL: `CREATE TABLE IF NOT EXISTS memories (
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
		Indexes: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_scope_fingerprint
				ON memories(scope_key, fingerprint)`,
			`CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope_key, id)`,
			// 按归属人检索（"这个人在本群说过什么"）。
			`CREATE INDEX IF NOT EXISTS idx_memories_scope_subject
				ON memories(scope_key, subject_id, id)`,
		},
		Columns: []columnSpec{
			{Table: "memories", Name: "score", DDL: "score REAL NOT NULL DEFAULT 0"},
			{Table: "memories", Name: "title", DDL: "title TEXT NOT NULL DEFAULT ''"},
			{Table: "memories", Name: "source_refs", DDL: "source_refs TEXT NOT NULL DEFAULT '[]'"},
			// subject_id 是这条记忆**关于谁**（群聊里就是发言人的 QQ 号）。
			// 记忆按作用域（群）共享，但一条事实总有归属人；把它做成结构化字段
			// 而不是让模型在自然语言里写主语，才能做到"看得到整个群、也查得到某个人"。
			// 0 表示未指明归属（旧数据与无法判定的场景）。
			{Table: "memories", Name: "subject_id", DDL: "subject_id INTEGER NOT NULL DEFAULT 0"},
		},
	},
	{
		// 在途操作（F-86）：等待下一条消息、等待人工审批。
		//
		// 存在这张表里的理由是「重启不该让用户悬在空中」：一次审批可能已经问过人了，
		// 确认回来时进程却已经忘了在等什么。
		//
		// 记录不随完成而删除，只改状态：谁在什么时候批准/拒绝/超时，正是审计要回答的。
		// 表的有界性由 PrunePending 保证。
		CreateSQL: `CREATE TABLE IF NOT EXISTS pending (
			id          TEXT PRIMARY KEY,
			session_key TEXT    NOT NULL,
			kind        TEXT    NOT NULL,
			payload     TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			expires_at  INTEGER NOT NULL DEFAULT 0,
			status      TEXT    NOT NULL,
			note        TEXT    NOT NULL DEFAULT ''
		)`,
		Indexes: []string{
			`CREATE INDEX IF NOT EXISTS idx_pending_status ON pending(status, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_pending_session ON pending(session_key)`,
		},
	},
	{
		// 提示词快照（F-89）。
		//
		// 存的是消息指纹而不是正文：正文可能含隐私，而前缀比较只需要判断
		// 「这一段是否逐字节相同」。逐条指纹还让「分歧发生在哪一条」可直接算出。
		//
		// relation 落库是必要的：只记录指纹而不记录「和上一条比怎么样了」，
		// 事后就无法回答「前缀是从哪一轮开始不稳的」。
		CreateSQL: `CREATE TABLE IF NOT EXISTS prompt_snapshots (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key   TEXT    NOT NULL,
			seq           INTEGER NOT NULL,
			digest        TEXT    NOT NULL,
			memory_digest TEXT    NOT NULL DEFAULT '',
			message_count INTEGER NOT NULL DEFAULT 0,
			relation      TEXT    NOT NULL DEFAULT '',
			common_prefix INTEGER NOT NULL DEFAULT 0,
			slid_by       INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL
		)`,
		Indexes: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_snapshots_session_seq
				ON prompt_snapshots(session_key, seq)`,
		},
		Columns: []columnSpec{
			{Table: "prompt_snapshots", Name: "memory_digest", DDL: "memory_digest TEXT NOT NULL DEFAULT ''"},
		},
	},
	{
		// 会话人格（F-82）：scoped.SessionRef.String() -> 人格名。
		//
		// 单独成表而不是并进会话台账：人格是**配置状态**，台账是**计量状态**，
		// 两者的写入频率与保留策略都不同；混在一张表里，"清空用量"就会顺手
		// 清掉人格，而那是用户显式设定的东西。
		//
		// 不设外键指向 prompts/personas 目录：人格是文件资产，不在库里，
		// 引用是否有效由启动期校验负责（F-25 fail-fast）。
		CreateSQL: `CREATE TABLE IF NOT EXISTS session_personas (
			session_key TEXT PRIMARY KEY,
			persona     TEXT NOT NULL,
			updated_at  INTEGER NOT NULL
		)`,
	},
	{
		// 成本聚合快照（F-66 的持久化那一条）。
		//
		// 存整份快照而不是逐事件流水：本表要回答的是"这个周期已经用了多少"，
		// 那是聚合量。逐条流水是另一张表的事（会话台账 F-85 已有用量列），
		// 两者混在一起会让"重启后配额从零开始"这种 bug 更难发现。
		//
		// id 固定为 1：快照是覆盖写，历史值没有意义，反而会让加载时选错版本。
		CreateSQL: `CREATE TABLE IF NOT EXISTS cost_snapshot (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			data       BLOB    NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	{
		// 分层记忆（F-49）的四张表。
		//
		// id 全部来自 tier_ids 这一个分配器，而不是各表自己的 AUTOINCREMENT：
		// MemTierStore 用的是一个全局单调计数器，而 Promote(id)/DeleteEpisodeItem(id)
		// 都假设 id 在三层之间唯一。分表各自编号会让"working 的 5 与 semantic 的 5"
		// 撞在一起，Promote 就会提升错条目——这类错不会报错，只会记错东西。
		CreateSQL: `CREATE TABLE IF NOT EXISTS tier_ids (
			id INTEGER PRIMARY KEY AUTOINCREMENT
		)`,
		Indexes: nil,
	},
	{
		CreateSQL: `CREATE TABLE IF NOT EXISTS tier_episodes (
			id         INTEGER PRIMARY KEY,
			scope_key  TEXT    NOT NULL,
			started_at INTEGER NOT NULL,
			ended_at   INTEGER NOT NULL
		)`,
		Indexes: []string{
			`CREATE INDEX IF NOT EXISTS idx_tier_episodes_scope ON tier_episodes(scope_key, id)`,
		},
	},
	{
		// tier 取 working | episodic | semantic；episodic 行用 episode_id 归组。
		CreateSQL: `CREATE TABLE IF NOT EXISTS tier_items (
			id          INTEGER PRIMARY KEY,
			scope_key   TEXT    NOT NULL,
			tier        TEXT    NOT NULL,
			episode_id  INTEGER NOT NULL DEFAULT 0,
			text        TEXT    NOT NULL,
			title       TEXT    NOT NULL DEFAULT '',
			refs        TEXT    NOT NULL DEFAULT '[]',
			score       REAL    NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL DEFAULT 0,
			fingerprint TEXT    NOT NULL DEFAULT ''
		)`,
		Indexes: []string{
			`CREATE INDEX IF NOT EXISTS idx_tier_items_scope_tier ON tier_items(scope_key, tier, id)`,
			`CREATE INDEX IF NOT EXISTS idx_tier_items_episode ON tier_items(episode_id, id)`,
		},
	},
}

// schemaStatements 把 tables 展平成**建表 + 触发器**语句序列，顺序与注册顺序一致。
//
// **索引不在这里**：见 indexStatements。
func schemaStatements() []string {
	out := make([]string, 0, 16)
	for _, t := range tables {
		out = append(out, t.CreateSQL)
		out = append(out, t.Attached...)
	}
	return out
}

// indexStatements 汇总全部索引语句。
//
// 索引**必须**在建表与补列之后执行：CREATE TABLE IF NOT EXISTS 对已存在的表是空操作，
// 因此给旧库新增一个"带索引的列"时，若索引与建表同批执行，就会在列还没加上的时候
// 去引用它——报 "no such column"。这是加列这一整类场景的结构性顺序要求，
// 不是某一条索引的特例。
func indexStatements() []string {
	out := make([]string, 0, 16)
	for _, t := range tables {
		out = append(out, t.Indexes...)
	}
	return out
}

// declaredColumns 汇总所有表声明式维护的列，顺序与建表顺序一致。
func declaredColumns() []columnSpec {
	var out []columnSpec
	for _, t := range tables {
		out = append(out, t.Columns...)
	}
	return out
}
