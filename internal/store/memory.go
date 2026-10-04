package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/textsim"
)

// ErrMemoryNotFound 表示记忆不存在。
var ErrMemoryNotFound = errors.New("memory not found")

// Memory 是一条长期记忆。
type Memory struct {
	ID         int64
	ScopeKey   string
	Kind       string
	Title      string
	Text       string
	SourceRefs string // JSON 数组文本，指向原始对话
	CreatedAt  int64
	UpdatedAt  int64
	Score      float64
	// Fingerprint 是作用域 + 正文的指纹，用于幂等写入。
	Fingerprint string
	// SubjectID 是这条记忆**关于谁**：群聊里是发言人的 QQ 号，0 表示未指明。
	// 作用域（群）决定''谁看得到''，SubjectID 决定''这是谁的事''——两者不同维度。
	SubjectID int64
}

// MemoryDecision 是写入决策的结论。
type MemoryDecision string

// 写入决策取值。
const (
	MemoryAdded   MemoryDecision = "added"
	MemoryUpdated MemoryDecision = "updated"
	MemoryIgnored MemoryDecision = "ignored"
)

// MemoryWriteResult 说明这次写入做了什么，以及**为什么**。
//
// 带理由是刻意的：去重判据一旦变得不可解释，阈值就只能靠猜，
// 而误合并会静默吞掉一条真实记忆——那是最难发现的一类数据损失。
type MemoryWriteResult struct {
	Decision   MemoryDecision
	ID         int64
	MatchedID  int64
	Similarity float64
	Reason     string
}

// MemoryWriteOptions 允许上层覆盖存储层的默认判定。
//
// 存在的理由是：字符相似度做不了语义判断（"旧的一条"与"新的一条"相似度正好 0.50，
// 却是两件事）。当上层用更可靠的手段（例如不开思考的模型）得出结论后，
// 需要能把结论传进来，而不是让存储层再猜一次。
type MemoryWriteOptions struct {
	// ForceMergeID > 0 时无条件并入该条目（上层判定为同一件事）。
	ForceMergeID int64
	// ForceAdd 为 true 时无条件新增（上层判定为两件事）。
	ForceAdd bool
}

// FindSimilarMemory 返回作用域内与新文本最相似的记忆及其相似度。
//
// 导出它是为了让上层能在**歧义带**里自己做判断，而不是只能接受存储层的阈值。
func (s *Store) FindSimilarMemory(ctx context.Context, scopeKey, text string) (Memory, float64, bool, error) {
	return s.FindSimilarMemoryFor(ctx, scopeKey, 0, text)
}

// FindSimilarMemoryFor 只在**同一个归属人**的既有记忆里找最相似条目。
//
// subjectID 为 0 时匹配"未指明归属"的条目，与实际写入时的语义一致。
// 限定归属人是必要的：不限定的话「张三很怕辣」会与「李四很怕辣」判为相似而合并，
// 把两个人的事混成一条——那是无法事后拆开的数据损失。
func (s *Store) FindSimilarMemoryFor(ctx context.Context, scopeKey string, subjectID int64, text string) (Memory, float64, bool, error) {
	scope := strings.TrimSpace(scopeKey)
	if scope == "" || strings.TrimSpace(text) == "" {
		return Memory{}, 0, false, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memoryCols+` FROM memories WHERE scope_key = ? AND subject_id = ? ORDER BY id ASC`,
		scope, subjectID)
	if err != nil {
		return Memory{}, 0, false, fmt.Errorf("find similar memory: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		best Memory
		sim  float64
	)
	for rows.Next() {
		c, err := scanMemory(rows)
		if err != nil {
			return Memory{}, 0, false, fmt.Errorf("scan memory: %w", err)
		}
		if s := textsim.Similarity(text, c.Text); s > sim {
			best, sim = c, s
		}
	}
	if err := rows.Err(); err != nil {
		return Memory{}, 0, false, fmt.Errorf("iterate memories: %w", err)
	}
	return best, sim, sim > 0, nil
}

// SaveMemory 用存储层的默认判据写入一条记忆（等价于不带覆盖项）。
func (s *Store) SaveMemory(ctx context.Context, m Memory) (MemoryWriteResult, error) {
	return s.SaveMemoryWith(ctx, m, MemoryWriteOptions{})
}

// SaveMemoryWith 按"新增 / 更新 / 忽略"三态写入一条记忆（F-87）。
//
// 判定顺序：
//  1. 作用域 + 正文 的指纹完全命中 -> 忽略（同一条，重放幂等）
//  2. 调用方指定了 ForceMergeID / ForceAdd -> 按调用方的结论办
//  3. 与同作用域内某条相似度 >= 阈值 -> **就地更新**那条（保持 id 与顺序不变）
//  4. 否则新增
//
// 就地更新而非"删旧插新"是因为记忆段的渲染顺序必须稳定：
// 新增会拿到新 id 并排到末尾，让整段顺序位移，平白多失效一次前缀缓存。
func (s *Store) SaveMemoryWith(ctx context.Context, m Memory, opts MemoryWriteOptions) (MemoryWriteResult, error) {
	var res MemoryWriteResult
	scope := strings.TrimSpace(m.ScopeKey)
	text := strings.TrimSpace(m.Text)
	if scope == "" {
		return res, fmt.Errorf("save memory: scope key must not be empty")
	}
	if text == "" {
		return res, fmt.Errorf("save memory: text must not be empty")
	}
	if m.Kind == "" {
		m.Kind = "fact"
	}
	// 指纹把**归属人**也算进去：否则「张三很怕辣」与「李四很怕辣」文本相同、
	// 指纹相同，第二条会被幂等判据当成重复丢弃——归属不同却被当成同一条。
	m.Fingerprint = Fingerprint(scope, strconv.FormatInt(m.SubjectID, 10), text)

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = MemoryWriteResult{}

		// 1) 指纹命中：完全同一条。
		var existingID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM memories WHERE scope_key = ? AND fingerprint = ?`,
			scope, m.Fingerprint).Scan(&existingID)
		if err == nil {
			res = MemoryWriteResult{Decision: MemoryIgnored, ID: existingID, MatchedID: existingID,
				Similarity: 1, Reason: "内容指纹相同，视为同一条"}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check memory fingerprint: %w", err)
		}

		// 2) 找最相似的既有条目。
		//
		// **只在同一个归属人内比较**：跨归属人合并会把两个人的事混成一条
		// （「张三很怕辣」+「李四很怕辣」），而那是无法事后拆开的数据损失。
		// 无归属（subject_id = 0）的记忆自成一组，同样不与他人混合。
		candidates, err := memoryRowsForSubject(ctx, tx, scope, m.SubjectID)
		if err != nil {
			return err
		}
		var (
			bestID   int64
			bestText string
			bestSim  float64
		)
		for _, c := range candidates {
			sim := textsim.Similarity(text, c.Text)
			if sim > bestSim {
				bestID, bestText, bestSim = c.ID, c.Text, sim
			}
		}
		now := nowMillis()

		// 调用方（例如语义判官）给了明确结论时，尊重它——但只信它指向的 id 确实存在。
		if opts.ForceMergeID > 0 {
			var exists int64
			if err := tx.QueryRowContext(ctx,
				`SELECT id FROM memories WHERE scope_key = ? AND id = ?`,
				scope, opts.ForceMergeID).Scan(&exists); err == nil {
				bestID = opts.ForceMergeID
				if _, err := tx.ExecContext(ctx,
					`UPDATE memories SET text = ?, fingerprint = ?, updated_at = ?,`+
						` subject_id = CASE WHEN subject_id = 0 THEN ? ELSE subject_id END WHERE id = ?`,
					text, m.Fingerprint, now, m.SubjectID, bestID); err != nil {
					return fmt.Errorf("update memory: %w", err)
				}
				res = MemoryWriteResult{Decision: MemoryUpdated, ID: bestID, MatchedID: bestID,
					Similarity: bestSim, Reason: fmt.Sprintf("上层判定为同一件事，并入 #%d", bestID)}
				return nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("verify merge target: %w", err)
			}
			// 目标不存在（可能刚被删）：退回默认判定，不猜。
		}
		if opts.ForceAdd {
			id, err := insertMemory(ctx, tx, scope, m, text)
			if err != nil {
				return err
			}
			res = MemoryWriteResult{Decision: MemoryAdded, ID: id, Similarity: bestSim,
				Reason: "上层判定为不同的事，新增"}
			return nil
		}

		if bestID != 0 && textsim.IsDuplicate(text, bestText) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE memories SET text = ?, fingerprint = ?, updated_at = ?,`+
					` subject_id = CASE WHEN subject_id = 0 THEN ? ELSE subject_id END WHERE id = ?`,
				text, m.Fingerprint, now, m.SubjectID, bestID); err != nil {
				return fmt.Errorf("update memory: %w", err)
			}
			res = MemoryWriteResult{Decision: MemoryUpdated, ID: bestID, MatchedID: bestID,
				Similarity: bestSim, Reason: fmt.Sprintf("与 #%d 相似度 %.2f，并入既有条目", bestID, bestSim)}
			return nil
		}

		id, err := insertMemory(ctx, tx, scope, m, text)
		if err != nil {
			return err
		}
		res = MemoryWriteResult{Decision: MemoryAdded, ID: id, Similarity: bestSim,
			Reason: "作用域内没有相似条目"}
		return nil
	})
	return res, err
}

// insertMemory 插入一条记忆，返回其 id。
func insertMemory(ctx context.Context, tx *sql.Tx, scope string, m Memory, text string) (int64, error) {
	created := m.CreatedAt
	if created == 0 {
		created = nowMillis()
	}
	r, err := tx.ExecContext(ctx,
		`INSERT INTO memories
		 (scope_key, kind, title, text, source_refs, created_at, updated_at, score, fingerprint, subject_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope, m.Kind, m.Title, text, m.SourceRefs, created, nowMillis(), m.Score, m.Fingerprint, m.SubjectID)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("memory id: %w", err)
	}
	return id, nil
}

// memoryRowsForSubject 取同作用域**同归属人**的既有条目，用于相似度去重。
//
// 限定归属人是刻意的：跨归属人合并会把两个人的事混成一条，而那是无法事后拆开的损失。
func memoryRowsForSubject(ctx context.Context, tx *sql.Tx, scope string, subjectID int64) ([]Memory, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+memoryCols+` FROM memories WHERE scope_key = ? AND subject_id = ? ORDER BY id ASC`,
		scope, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list memories for dedup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Memory, 0, 8)
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const memoryCols = "id, scope_key, kind, title, text, source_refs, created_at, updated_at, score, fingerprint, subject_id"

func scanMemory(sc interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	err := sc.Scan(&m.ID, &m.ScopeKey, &m.Kind, &m.Title, &m.Text, &m.SourceRefs,
		&m.CreatedAt, &m.UpdatedAt, &m.Score, &m.Fingerprint, &m.SubjectID)
	return m, err
}

// RecallMemories 按 id 升序返回作用域内的全部记忆。
//
// 顺序必须**确定**：同一份记忆两次渲染不同，会平白多失效一次前缀缓存（ADR-0002）。
func (s *Store) RecallMemories(ctx context.Context, scopeKey string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memoryCols+` FROM memories WHERE scope_key = ? ORDER BY id ASC`, scopeKey)
	if err != nil {
		return nil, fmt.Errorf("recall memories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Memory, 0, 8)
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return out, nil
}

// ListMemories 按更新时间倒序返回（供用户检视）。
func (s *Store) ListMemories(ctx context.Context, scopeKey string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memoryCols+` FROM memories WHERE scope_key = ?
		ORDER BY updated_at DESC, id DESC LIMIT ?`, scopeKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Memory, 0, limit)
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMemories 返回作用域内的条数。
func (s *Store) CountMemories(ctx context.Context, scopeKey string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM memories WHERE scope_key = ?`, scopeKey).Scan(&n); err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return n, nil
}

// ForgetMemory 删除一条记忆（幂等：删不存在的 id 不算错误）。
func (s *Store) ForgetMemory(ctx context.Context, scopeKey string, id int64) (bool, error) {
	var deleted bool
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM memories WHERE scope_key = ? AND id = ?`, scopeKey, id)
		if err != nil {
			return fmt.Errorf("forget memory: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("forget memory rows: %w", err)
		}
		deleted = n > 0
		return nil
	})
	return deleted, err
}

// ForgetScope 清空一个作用域的记忆。
func (s *Store) ForgetScope(ctx context.Context, scopeKey string) (int, error) {
	var n int64
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE scope_key = ?`, scopeKey)
		if err != nil {
			return fmt.Errorf("forget scope: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// TrimMemories 按 LRU + 分值淘汰到 keep 条（F-88 的留存策略）。
//
// 淘汰顺序：先按分值升序，再按更新时间升序，最后按 id——
// 同分同时间的顺序必须确定，否则每次淘汰的对象可能不同。
func (s *Store) TrimMemories(ctx context.Context, scopeKey string, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	var n int64
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		n = 0
		// 按**从好到差**排序后跳过前 keep 条，删掉其余——即删最差的。
		// 反过来写（ASC + OFFSET）会把分值最高的删掉，正好删反。
		res, err := tx.ExecContext(ctx,
			`DELETE FROM memories WHERE id IN (
				SELECT id FROM memories WHERE scope_key = ?
				ORDER BY score DESC, updated_at DESC, id DESC
				LIMIT -1 OFFSET ?
			)`, scopeKey, keep)
		if err != nil {
			return fmt.Errorf("trim memories: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// memoryFileRecord 是旧记忆 JSONL 的行格式。
//
// 刻意不复用 history 包的同名结构：history 依赖 store，反向引用会成环。
// 这里只声明需要的字段，格式不匹配时 Unmarshal 会失败而不是静默丢数据。
type memoryFileRecord struct {
	Key  string `json:"key"`
	Item struct {
		// history.Item 没有 json tag，因此字段名就是大写的原名。
		Content string `json:"Content"`
	} `json:"item"`
}

// ImportMemoriesJSONL 从旧的记忆文件导入（F-83 的迁移路径，幂等）。
//
// 幂等由指纹保证：同一条内容重放会被判为 ignored。
func (s *Store) ImportMemoriesJSONL(ctx context.Context, path string) (imported, skipped int, err error) {
	f, err := os.Open(path) //nolint:gosec // 路径来自运维显式配置
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("%w: %s", ErrImportSourceMissing, path)
		}
		return 0, 0, fmt.Errorf("open memory import source: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec memoryFileRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return imported, skipped, fmt.Errorf("parse line %d of %s: %w", lineNo, path, err)
		}
		scope := strings.TrimSpace(rec.Key)
		text := strings.TrimSpace(rec.Item.Content)
		if scope == "" || text == "" {
			continue
		}
		res, err := s.SaveMemory(ctx, Memory{ScopeKey: scope, Text: text})
		if err != nil {
			return imported, skipped, fmt.Errorf("import memory line %d: %w", lineNo, err)
		}
		if res.Decision == MemoryIgnored {
			skipped++
		} else {
			imported++
		}
	}
	if err := scanner.Err(); err != nil {
		return imported, skipped, fmt.Errorf("read memory import source: %w", err)
	}
	return imported, skipped, nil
}

// MemoryExportRecord 是导出的一行（字段与内部表一致，便于回读）。
type MemoryExportRecord struct {
	ScopeKey   string  `json:"scope_key"`
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Text       string  `json:"text"`
	SourceRefs string  `json:"source_refs"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
	Score      float64 `json:"score"`
}

// ExportMemories 把**全部作用域**的记忆写成 JSONL（F-88 的导出）。
//
// 导出会跨作用域，因此它是维护命令而非会话内能力——会话内只能看到自己的作用域。
func (s *Store) ExportMemories(ctx context.Context, w io.Writer) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memoryCols+` FROM memories ORDER BY scope_key ASC, id ASC`)
	if err != nil {
		return 0, fmt.Errorf("export memories: %w", err)
	}
	defer func() { _ = rows.Close() }()

	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return n, fmt.Errorf("scan memory for export: %w", err)
		}
		rec := MemoryExportRecord{
			ScopeKey: m.ScopeKey, ID: m.ID, Kind: m.Kind, Title: m.Title, Text: m.Text,
			SourceRefs: m.SourceRefs, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Score: m.Score,
		}
		if err := enc.Encode(rec); err != nil {
			return n, fmt.Errorf("encode memory: %w", err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("iterate memories for export: %w", err)
	}
	return n, nil
}

// MemoryScopes 返回存在记忆的全部作用域。
func (s *Store) MemoryScopes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT scope_key FROM memories ORDER BY scope_key ASC`)
	if err != nil {
		return nil, fmt.Errorf("list memory scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
