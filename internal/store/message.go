package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MinTrigramRunes 是 trigram 分词器能生效的最短查询长度。
//
// 实测（SQLite 3.53.4）：trigram 按 3 字符滑窗建索引，短于 3 字符的查询**命中为 0**。
// 中文里两字词（橙汁、天气、作业）恰恰最常见，所以查询侧必须按这个长度分流。
const MinTrigramRunes = 3

// ErrMessageNotFound 表示指定的消息不存在。
var ErrMessageNotFound = errors.New("message not found")

// Message 是 messages 表的一行。
type Message struct {
	ID          int64
	SessionKey  string
	Seq         int64
	Role        string
	Kind        string
	Content     string
	Name        string
	ToolCallID  string
	ToolCalls   string
	TokenCount  int64
	CreatedAt   int64
	Fingerprint string
	// SpeakerID / SpeakerName 标识发言人（群聊才有）。
	// 与正文分开存：正文保持干净，标签在渲染时拼。
	SpeakerID   int64
	SpeakerName string
}

// MessageHit 是一条检索命中：消息本体 + 片段 + 前后各一条。
type MessageHit struct {
	Message Message
	// Snippet 是命中处的上下文片段，命中词用 [] 标出。
	Snippet string
	Before  *Message
	After   *Message
}

const messageCols = "id, session_key, seq, role, kind, content, name, tool_call_id, tool_calls, token_count, created_at, fingerprint, speaker_id, speaker_name"

// messageColsQ 是带表名前缀的列清单。
//
// FTS 查询要 JOIN messages_fts 与 messages，两边都有 content 列——
// 不带前缀会直接报 "ambiguous column name: content"。
const messageColsQ = "messages.id, messages.session_key, messages.seq, messages.role, messages.kind, " +
	"messages.content, messages.name, messages.tool_call_id, messages.tool_calls, " +
	"messages.token_count, messages.created_at, messages.fingerprint, " +
	"messages.speaker_id, messages.speaker_name"

func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := sc.Scan(&m.ID, &m.SessionKey, &m.Seq, &m.Role, &m.Kind, &m.Content,
		&m.Name, &m.ToolCallID, &m.ToolCalls, &m.TokenCount, &m.CreatedAt, &m.Fingerprint,
		&m.SpeakerID, &m.SpeakerName)
	return m, err
}

// AppendMessage 追加一条消息。
//
// 序号语义：
//   - m.Seq > 0：使用给定序号（导入路径用）。若 (session_key, seq) 已存在则**跳过**并
//     返回 inserted=false——这让导入天然幂等，而不必靠"内容相同就丢"的粗糙判据
//     （对话里重复说同一句话是完全正常的）。
//   - m.Seq == 0：在事务内分配下一个序号。
//
// 分配发生在写事务内，而写事务由 Store 串行化，因此不会出现两条消息抢到同一序号。
func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, bool, error) {
	if strings.TrimSpace(m.SessionKey) == "" {
		return 0, false, fmt.Errorf("append message: session key must not be empty")
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = nowMillis()
	}
	if m.Fingerprint == "" {
		m.Fingerprint = Fingerprint(m.SessionKey, m.Role, m.Kind, m.Content)
	}

	var (
		id       int64
		inserted bool
	)
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, inserted = 0, false
		seq := m.Seq
		if seq <= 0 {
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_key = ?`,
				m.SessionKey).Scan(&seq); err != nil {
				return fmt.Errorf("next seq: %w", err)
			}
		} else {
			var exists int
			err := tx.QueryRowContext(ctx,
				`SELECT 1 FROM messages WHERE session_key = ? AND seq = ?`,
				m.SessionKey, seq).Scan(&exists)
			if err == nil {
				return nil // 已导入过，跳过
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check existing seq: %w", err)
			}
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO messages
			 (session_key, seq, role, kind, content, name, tool_call_id, tool_calls, token_count, created_at, fingerprint, speaker_id, speaker_name)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.SessionKey, seq, m.Role, m.Kind, m.Content, m.Name, m.ToolCallID, m.ToolCalls,
			m.TokenCount, m.CreatedAt, m.Fingerprint, m.SpeakerID, m.SpeakerName)
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("message id: %w", err)
		}
		inserted = true
		return nil
	})
	return id, inserted, err
}

// Messages 返回会话的消息，按 seq 升序。
//
// limit > 0 时返回**最近** limit 条（仍是升序，便于直接拼提示词）。
func (s *Store) Messages(ctx context.Context, sessionKey string, limit int) ([]Message, error) {
	q := `SELECT ` + messageCols + ` FROM messages WHERE session_key = ?`
	var args []any
	args = append(args, sessionKey)
	if limit > 0 {
		q += " ORDER BY seq DESC LIMIT ?"
		args = append(args, limit)
	} else {
		q += " ORDER BY seq ASC"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Message, 0, 16)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	if limit > 0 {
		reverseMessages(out)
	}
	return out, nil
}

// MessageCount 返回会话的消息条数。
func (s *Store) MessageCount(ctx context.Context, sessionKey string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE session_key = ?`, sessionKey).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return n, nil
}

// TotalMessageCount 返回全库消息条数（迁移判断用：库为空才导入旧文件）。
func (s *Store) TotalMessageCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count all messages: %w", err)
	}
	return n, nil
}

// DeleteMessage 删除一条消息（FTS 由触发器同步删除）。
func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete message: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrMessageNotFound
		}
		return nil
	})
}

// ResetMessages 清空一个会话的消息。
func (s *Store) ResetMessages(ctx context.Context, sessionKey string) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_key = ?`, sessionKey)
		if err != nil {
			return fmt.Errorf("reset messages: %w", err)
		}
		return nil
	})
}

// TrimMessages 保留会话最近 keep 条，删除更早的。
func (s *Store) TrimMessages(ctx context.Context, sessionKey string, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	var removed int
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE session_key = ? AND seq <= (
				SELECT COALESCE(MAX(seq), 0) - ? FROM messages WHERE session_key = ?
			)`, sessionKey, keep, sessionKey)
		if err != nil {
			return fmt.Errorf("trim messages: %w", err)
		}
		n, err := res.RowsAffected()
		if err == nil {
			removed = int(n)
		}
		return nil
	})
	return removed, err
}

// SearchMessages 按关键词检索会话历史（F-84）。
//
// 混合策略（经与用户确认）：
//   - 查询 >= MinTrigramRunes 个字符：走 FTS5(trigram)，快且能给出片段
//   - 更短：trigram 建不出索引，直接走 LIKE
//   - FTS 未命中或报错：兜底走一次 LIKE，确保"搜不到"不是分词器造成的假阴性
//
// 只查**给定会话**。空查询返回最近的 limit 条。
func (s *Store) SearchMessages(ctx context.Context, sessionKey, query string, limit int) ([]MessageHit, error) {
	if limit <= 0 {
		limit = 10
	}
	needle := strings.TrimSpace(query)
	if needle == "" {
		msgs, err := s.Messages(ctx, sessionKey, limit)
		if err != nil {
			return nil, err
		}
		hits := make([]MessageHit, 0, len(msgs))
		for _, m := range msgs {
			hits = append(hits, MessageHit{Message: m})
		}
		return s.withNeighbours(ctx, sessionKey, hits)
	}

	var (
		hits []MessageHit
		err  error
	)
	if utf8.RuneCountInString(needle) >= MinTrigramRunes {
		hits, err = s.searchFTS(ctx, sessionKey, needle, limit)
		if err != nil {
			// FTS 的语法或分词问题不该让用户搜不到，所以退回 LIKE。
			//
			// 但**绝不能静默**：这条回退曾在开发中掩盖了一个真实的 SQL 错误
			// （JOIN 里 content 列名有歧义），表面上"还能搜到"，实际 FTS 一次都没成功过。
			s.warn("fts search failed; falling back to LIKE: " + err.Error())
			hits, err = nil, nil
		}
	}
	if len(hits) == 0 {
		hits, err = s.searchLike(ctx, sessionKey, needle, limit)
		if err != nil {
			return nil, err
		}
	}
	return s.withNeighbours(ctx, sessionKey, hits)
}

func (s *Store) searchFTS(ctx context.Context, sessionKey, needle string, limit int) ([]MessageHit, error) {
	// 把整个查询当成**一个短语**：FTS5 的查询语法里 -、:、*、引号都会改变语义，
	// 实测 "chat-send" 会直接报 no such column: send。短语化后语法风险归零，
	// 而 trigram 下的短语匹配恰好等价于子串检索——正是我们要的语义。
	phrase := `"` + strings.ReplaceAll(needle, `"`, `""`) + `"`
	q := `SELECT ` + messageColsQ + `, snippet(messages_fts, 0, '[', ']', '…', 16)
		FROM messages_fts
		JOIN messages ON messages.id = messages_fts.rowid
		WHERE messages_fts MATCH ? AND messages.session_key = ?
		ORDER BY messages.seq DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, phrase, sessionKey, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]MessageHit, 0, limit)
	for rows.Next() {
		var (
			m       Message
			snippet string
		)
		if err := rows.Scan(&m.ID, &m.SessionKey, &m.Seq, &m.Role, &m.Kind, &m.Content,
			&m.Name, &m.ToolCallID, &m.ToolCalls, &m.TokenCount, &m.CreatedAt, &m.Fingerprint,
			&m.SpeakerID, &m.SpeakerName, &snippet); err != nil {
			return nil, fmt.Errorf("scan fts hit: %w", err)
		}
		out = append(out, MessageHit{Message: m, Snippet: snippet})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fts hits: %w", err)
	}
	// 由新到旧取最近 limit 条，再翻回时间序。
	reverseHits(out)
	return out, nil
}

func (s *Store) searchLike(ctx context.Context, sessionKey, needle string, limit int) ([]MessageHit, error) {
	q := `SELECT ` + messageCols + ` FROM messages
		WHERE session_key = ? AND content LIKE ? ESCAPE '\'
		ORDER BY seq DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, sessionKey, "%"+escapeLike(needle)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("like search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]MessageHit, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan like hit: %w", err)
		}
		out = append(out, MessageHit{Message: m, Snippet: snippetAround(m.Content, needle, 16)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate like hits: %w", err)
	}
	reverseHits(out)
	return out, nil
}

// withNeighbours 给每条命中补上前后各一条消息。
func (s *Store) withNeighbours(ctx context.Context, sessionKey string, hits []MessageHit) ([]MessageHit, error) {
	for i := range hits {
		before, err := s.messageBySeq(ctx, sessionKey, hits[i].Message.Seq-1)
		if err != nil {
			return nil, err
		}
		after, err := s.messageBySeq(ctx, sessionKey, hits[i].Message.Seq+1)
		if err != nil {
			return nil, err
		}
		hits[i].Before = before
		hits[i].After = after
	}
	return hits, nil
}

func (s *Store) messageBySeq(ctx context.Context, sessionKey string, seq int64) (*Message, error) {
	if seq <= 0 {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM messages WHERE session_key = ? AND seq = ?`,
		sessionKey, seq)
	m, err := scanMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("neighbour message: %w", err)
	}
	return &m, nil
}

// escapeLike 转义 LIKE 的通配符（配合 ESCAPE '\'）。
//
// NewReplacer 单趟替换、不回头扫描，因此不需要担心把刚插入的反斜杠再转义一次。
func escapeLike(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(s)
}

// snippetAround 截取命中处两侧的上下文（LIKE 路径用；FTS 路径由 snippet() 提供）。
func snippetAround(content, needle string, radius int) string {
	runes := []rune(content)
	byteIdx := strings.Index(content, needle)
	if byteIdx < 0 {
		byteIdx = strings.Index(strings.ToLower(content), strings.ToLower(needle))
	}
	if byteIdx < 0 {
		return truncateRunes(content, radius*2)
	}

	head := utf8.RuneCountInString(content[:byteIdx])
	start := head - radius
	if start < 0 {
		start = 0
	}
	end := head + utf8.RuneCountInString(needle) + radius
	if end > len(runes) {
		end = len(runes)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func reverseMessages(m []Message) {
	for i, j := 0, len(m)-1; i < j; i, j = i+1, j-1 {
		m[i], m[j] = m[j], m[i]
	}
}

func reverseHits(h []MessageHit) {
	for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
		h[i], h[j] = h[j], h[i]
	}
}
