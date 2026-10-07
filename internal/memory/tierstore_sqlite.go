package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/drysaltyfish/agentbot/internal/store"
)

// SQLiteTierStore 是 TierStore 的持久化实现（F-49 / F-83）。
//
// 它把分层记忆落到 SQLite，从而消除"进程重启即丢记忆"的行为退化——
// 那是内存实现 MemTierStore 唯一不适合生产的地方。
//
// 写入一律走 store.Write（单写者纪律），读取走 store.Read（共享连接池）；
// id 全部来自 tier_ids 一个分配器，保证三层之间 id 唯一。
type SQLiteTierStore struct {
	st *store.Store
}

// NewSQLiteTierStore 构造；st 为 nil 时所有方法都会明确报错，
// 而不是静默退回内存——静默退回正是"重启丢记忆"最难查的形态。
func NewSQLiteTierStore(st *store.Store) *SQLiteTierStore { return &SQLiteTierStore{st: st} }

// rowQuerier 让查询逻辑同时适用于 *sql.DB 与 *sql.Tx。
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const workingColumns = `id, text, title, refs, score, created_at, subject_id`

func (s *SQLiteTierStore) ready() error {
	if s == nil || s.st == nil {
		return fmt.Errorf("memory: tier store is not configured")
	}
	return nil
}

// allocID 从全局分配器取一个新 id。必须在写事务里调用。
func (s *SQLiteTierStore) allocID(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO tier_ids DEFAULT VALUES`)
	if err != nil {
		return 0, fmt.Errorf("allocate tier id: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read tier id: %w", err)
	}
	return id, nil
}

func refsJSON(refs []string) string {
	if len(refs) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func parseRefs(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func queryItems(ctx context.Context, q rowQuerier, query string, args ...any) ([]TierItem, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tier items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TierItem
	for rows.Next() {
		var (
			it      TierItem
			refs    string
			created int64
		)
		if err := rows.Scan(&it.ID, &it.Text, &it.Title, &refs, &it.Score, &created, &it.SubjectID); err != nil {
			return nil, fmt.Errorf("scan tier item: %w", err)
		}
		it.Refs = parseRefs(refs)
		it.CreatedAt = time.UnixMilli(created)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tier items: %w", err)
	}
	return out, nil
}

func setTier(items []TierItem, tier Tier) []TierItem {
	for i := range items {
		items[i].Tier = tier
	}
	return items
}

// AppendWorking 实现 TierStore。
func (s *SQLiteTierStore) AppendWorking(ctx context.Context, scope string, item TierItem) (TierItem, error) {
	if err := s.ready(); err != nil {
		return TierItem{}, err
	}
	now := time.Now()
	item.Tier = TierWorking
	item.Refs = append([]string(nil), item.Refs...)
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, aerr := s.allocID(ctx, tx)
		if aerr != nil {
			return aerr
		}
		item.ID = id
		_, xerr := tx.ExecContext(ctx, `INSERT INTO tier_items
            (id, scope_key, tier, episode_id, text, title, refs, score, created_at, updated_at, fingerprint, subject_id)
            VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, '', ?)`,
			id, scope, TierWorking.String(), item.Text, item.Title, refsJSON(item.Refs), item.Score,
			item.CreatedAt.UnixMilli(), now.UnixMilli(), item.SubjectID)
		if xerr != nil {
			return fmt.Errorf("insert working item: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return TierItem{}, err
	}
	return item, nil
}

// Working 实现 TierStore：按 id 升序（即创建顺序）。
func (s *SQLiteTierStore) Working(ctx context.Context, scope string) ([]TierItem, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var out []TierItem
	err := s.st.Read(ctx, func(ctx context.Context, db *sql.DB) error {
		items, qerr := queryItems(ctx, db,
			`SELECT `+workingColumns+` FROM tier_items WHERE scope_key = ? AND tier = ? ORDER BY id ASC`,
			scope, TierWorking.String())
		if qerr != nil {
			return qerr
		}
		out = setTier(items, TierWorking)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrimWorking 实现 TierStore：保留最新 keep 条，返回被裁掉的旧条目。
func (s *SQLiteTierStore) TrimWorking(ctx context.Context, scope string, keep int) ([]TierItem, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if keep < 0 {
		keep = 0
	}
	var removed []TierItem
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		items, qerr := queryItems(ctx, tx,
			`SELECT `+workingColumns+` FROM tier_items WHERE scope_key = ? AND tier = ? ORDER BY id ASC`,
			scope, TierWorking.String())
		if qerr != nil {
			return qerr
		}
		if len(items) <= keep {
			removed = nil
			return nil
		}
		cut := len(items) - keep
		removed = setTier(items[:cut], TierWorking)
		for i := range removed {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_items WHERE id = ?`, removed[i].ID); derr != nil {
				return fmt.Errorf("trim working: %w", derr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// DeleteWorking 实现 TierStore。
func (s *SQLiteTierStore) DeleteWorking(ctx context.Context, scope string, id int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var deleted bool
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx,
			`DELETE FROM tier_items WHERE scope_key = ? AND tier = ? AND id = ?`,
			scope, TierWorking.String(), id)
		if xerr != nil {
			return fmt.Errorf("delete working item: %w", xerr)
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("delete working rows: %w", rerr)
		}
		deleted = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// UpdateWorking 实现 TierStore：就地改写正文与归属人，保留 id 与创建顺序。
func (s *SQLiteTierStore) UpdateWorking(ctx context.Context, scope string, id int64, item TierItem) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var updated bool
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx,
			`UPDATE tier_items SET text = ?, subject_id = ?, updated_at = ?
			 WHERE scope_key = ? AND tier = ? AND id = ?`,
			item.Text, item.SubjectID, time.Now().UnixMilli(), scope, TierWorking.String(), id)
		if xerr != nil {
			return fmt.Errorf("update working item: %w", xerr)
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("update working rows: %w", rerr)
		}
		updated = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

// AppendEpisode 实现 TierStore：片段与条目共用全局 id 分配器。
func (s *SQLiteTierStore) AppendEpisode(ctx context.Context, scope string, ep Episode) (Episode, error) {
	if err := s.ready(); err != nil {
		return Episode{}, err
	}
	now := time.Now()
	ep.Tier = TierEpisodic
	ep.Items = append([]TierItem(nil), ep.Items...)
	if ep.StartedAt.IsZero() {
		ep.StartedAt = now
	}
	if ep.EndedAt.IsZero() {
		ep.EndedAt = ep.StartedAt
	}
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, aerr := s.allocID(ctx, tx)
		if aerr != nil {
			return aerr
		}
		ep.ID = id
		if _, xerr := tx.ExecContext(ctx,
			`INSERT INTO tier_episodes (id, scope_key, started_at, ended_at) VALUES (?, ?, ?, ?)`,
			id, scope, ep.StartedAt.UnixMilli(), ep.EndedAt.UnixMilli()); xerr != nil {
			return fmt.Errorf("insert episode: %w", xerr)
		}
		for i := range ep.Items {
			ep.Items[i].Tier = TierEpisodic
			if ep.Items[i].ID == 0 {
				itemID, aerr := s.allocID(ctx, tx)
				if aerr != nil {
					return aerr
				}
				ep.Items[i].ID = itemID
			}
			if ep.Items[i].CreatedAt.IsZero() {
				ep.Items[i].CreatedAt = now
			}
			if _, xerr := tx.ExecContext(ctx, `INSERT INTO tier_items
                (id, scope_key, tier, episode_id, text, title, refs, score, created_at, updated_at, fingerprint, subject_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
				ep.Items[i].ID, scope, TierEpisodic.String(), id, ep.Items[i].Text, ep.Items[i].Title,
				refsJSON(ep.Items[i].Refs), ep.Items[i].Score, ep.Items[i].CreatedAt.UnixMilli(),
				now.UnixMilli(), ep.Items[i].SubjectID); xerr != nil {
				return fmt.Errorf("insert episode item: %w", xerr)
			}
		}
		return nil
	})
	if err != nil {
		return Episode{}, err
	}
	return ep, nil
}

// Episodes 实现 TierStore：按开始时间升序；limit>0 时取最新 limit 个。
func (s *SQLiteTierStore) Episodes(ctx context.Context, scope string, limit int) ([]Episode, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var out []Episode
	err := s.st.Read(ctx, func(ctx context.Context, db *sql.DB) error {
		rows, qerr := db.QueryContext(ctx,
			`SELECT id, started_at, ended_at FROM tier_episodes WHERE scope_key = ? ORDER BY id ASC`, scope)
		if qerr != nil {
			return fmt.Errorf("query episodes: %w", qerr)
		}
		var eps []Episode
		for rows.Next() {
			var (
				ep             Episode
				started, ended int64
			)
			if serr := rows.Scan(&ep.ID, &started, &ended); serr != nil {
				_ = rows.Close()
				return fmt.Errorf("scan episode: %w", serr)
			}
			ep.Tier = TierEpisodic
			ep.StartedAt = time.UnixMilli(started)
			ep.EndedAt = time.UnixMilli(ended)
			eps = append(eps, ep)
		}
		if rerr := rows.Err(); rerr != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate episodes: %w", rerr)
		}
		_ = rows.Close()

		if limit > 0 && limit < len(eps) {
			eps = eps[len(eps)-limit:]
		}
		for i := range eps {
			items, ierr := queryItems(ctx, db,
				`SELECT `+workingColumns+` FROM tier_items WHERE episode_id = ? ORDER BY id ASC`, eps[i].ID)
			if ierr != nil {
				return ierr
			}
			eps[i].Items = setTier(items, TierEpisodic)
		}
		out = eps
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrimEpisodes 实现 TierStore：淘汰最旧的若干片段连同其条目。
func (s *SQLiteTierStore) TrimEpisodes(ctx context.Context, scope string, keep int) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if keep < 0 {
		keep = 0
	}
	dropped := 0
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx,
			`SELECT id FROM tier_episodes WHERE scope_key = ? ORDER BY id ASC`, scope)
		if qerr != nil {
			return fmt.Errorf("query episode ids: %w", qerr)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if serr := rows.Scan(&id); serr != nil {
				_ = rows.Close()
				return fmt.Errorf("scan episode id: %w", serr)
			}
			ids = append(ids, id)
		}
		if rerr := rows.Err(); rerr != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate episode ids: %w", rerr)
		}
		_ = rows.Close()
		if len(ids) <= keep {
			dropped = 0
			return nil
		}
		cut := len(ids) - keep
		dropped = cut
		for _, id := range ids[:cut] {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_items WHERE episode_id = ?`, id); derr != nil {
				return fmt.Errorf("trim episode items: %w", derr)
			}
			if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_episodes WHERE id = ?`, id); derr != nil {
				return fmt.Errorf("trim episode: %w", derr)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return dropped, nil
}

// DeleteEpisode 实现 TierStore。
func (s *SQLiteTierStore) DeleteEpisode(ctx context.Context, scope string, id int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var deleted bool
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx,
			`DELETE FROM tier_episodes WHERE scope_key = ? AND id = ?`, scope, id)
		if xerr != nil {
			return fmt.Errorf("delete episode: %w", xerr)
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("delete episode rows: %w", rerr)
		}
		if n == 0 {
			deleted = false
			return nil
		}
		if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_items WHERE episode_id = ?`, id); derr != nil {
			return fmt.Errorf("delete episode items: %w", derr)
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// DeleteEpisodeItem 实现 TierStore：条目清空时一并删除片段。
func (s *SQLiteTierStore) DeleteEpisodeItem(ctx context.Context, scope string, itemID int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var found bool
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var episodeID int64
		qerr := tx.QueryRowContext(ctx,
			`SELECT episode_id FROM tier_items WHERE scope_key = ? AND tier = ? AND id = ?`,
			scope, TierEpisodic.String(), itemID).Scan(&episodeID)
		if qerr == sql.ErrNoRows {
			found = false
			return nil
		}
		if qerr != nil {
			return fmt.Errorf("locate episode item: %w", qerr)
		}
		found = true
		if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_items WHERE id = ?`, itemID); derr != nil {
			return fmt.Errorf("delete episode item: %w", derr)
		}
		var remaining int
		if cerr := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tier_items WHERE episode_id = ?`, episodeID).Scan(&remaining); cerr != nil {
			return fmt.Errorf("count episode items: %w", cerr)
		}
		if remaining == 0 {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_episodes WHERE id = ?`, episodeID); derr != nil {
				return fmt.Errorf("delete empty episode: %w", derr)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// UpsertSemantic 实现 TierStore：按 (scope, 文本指纹) 就地更新，不改变顺序。
func (s *SQLiteTierStore) UpsertSemantic(ctx context.Context, scope string, item TierItem) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	now := time.Now()
	key := store.Fingerprint("semantic", item.Text)
	added := false
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var existingID int64
		qerr := tx.QueryRowContext(ctx,
			`SELECT id FROM tier_items WHERE scope_key = ? AND tier = ? AND fingerprint = ? ORDER BY id ASC LIMIT 1`,
			scope, TierSemantic.String(), key).Scan(&existingID)
		switch {
		case qerr == nil:
			_, uerr := tx.ExecContext(ctx, `UPDATE tier_items
                SET text = ?, title = ?, refs = ?, score = ?, subject_id = ?, updated_at = ?
                WHERE id = ?`,
				item.Text, item.Title, refsJSON(item.Refs), item.Score, item.SubjectID, now.UnixMilli(), existingID)
			if uerr != nil {
				return fmt.Errorf("update semantic item: %w", uerr)
			}
			added = false
			return nil
		case qerr != sql.ErrNoRows:
			return fmt.Errorf("lookup semantic item: %w", qerr)
		}
		id, aerr := s.allocID(ctx, tx)
		if aerr != nil {
			return aerr
		}
		createdAt := item.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		if _, xerr := tx.ExecContext(ctx, `INSERT INTO tier_items
            (id, scope_key, tier, episode_id, text, title, refs, score, created_at, updated_at, fingerprint, subject_id)
            VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, scope, TierSemantic.String(), item.Text, item.Title, refsJSON(item.Refs), item.Score,
			createdAt.UnixMilli(), now.UnixMilli(), key, item.SubjectID); xerr != nil {
			return fmt.Errorf("insert semantic item: %w", xerr)
		}
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return added, nil
}

// Semantics 实现 TierStore：按写入顺序（id 升序）。
func (s *SQLiteTierStore) Semantics(ctx context.Context, scope string) ([]TierItem, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var out []TierItem
	err := s.st.Read(ctx, func(ctx context.Context, db *sql.DB) error {
		items, qerr := queryItems(ctx, db,
			`SELECT `+workingColumns+` FROM tier_items WHERE scope_key = ? AND tier = ? ORDER BY id ASC`,
			scope, TierSemantic.String())
		if qerr != nil {
			return qerr
		}
		out = setTier(items, TierSemantic)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrimSemantics 实现 TierStore：低分优先淘汰，同分淘汰更旧的，幸存者保持原顺序。
func (s *SQLiteTierStore) TrimSemantics(ctx context.Context, scope string, keep int) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if keep < 0 {
		keep = 0
	}
	dropped := 0
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx,
			`SELECT id, score, created_at FROM tier_items WHERE scope_key = ? AND tier = ? ORDER BY id ASC`,
			scope, TierSemantic.String())
		if qerr != nil {
			return fmt.Errorf("query semantics: %w", qerr)
		}
		type row struct {
			id        int64
			score     float64
			createdAt int64
		}
		var list []row
		for rows.Next() {
			var r row
			if serr := rows.Scan(&r.id, &r.score, &r.createdAt); serr != nil {
				_ = rows.Close()
				return fmt.Errorf("scan semantic: %w", serr)
			}
			list = append(list, r)
		}
		if rerr := rows.Err(); rerr != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate semantics: %w", rerr)
		}
		_ = rows.Close()
		if len(list) <= keep {
			dropped = 0
			return nil
		}
		order := make([]int, len(list))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			ia, ib := list[order[a]], list[order[b]]
			if ia.score != ib.score {
				return ia.score < ib.score
			}
			if ia.createdAt != ib.createdAt {
				return ia.createdAt < ib.createdAt
			}
			return ia.id < ib.id
		})
		cut := len(list) - keep
		dropped = cut
		for _, idx := range order[:cut] {
			if _, derr := tx.ExecContext(ctx, `DELETE FROM tier_items WHERE id = ?`, list[idx].id); derr != nil {
				return fmt.Errorf("trim semantic: %w", derr)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return dropped, nil
}

// DeleteSemantic 实现 TierStore。
func (s *SQLiteTierStore) DeleteSemantic(ctx context.Context, scope string, id int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	var deleted bool
	err := s.st.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx,
			`DELETE FROM tier_items WHERE scope_key = ? AND tier = ? AND id = ?`,
			scope, TierSemantic.String(), id)
		if xerr != nil {
			return fmt.Errorf("delete semantic item: %w", xerr)
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return fmt.Errorf("delete semantic rows: %w", rerr)
		}
		deleted = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

var _ TierStore = (*SQLiteTierStore)(nil)
