package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrPendingNotFound 表示在途记录不存在。
var ErrPendingNotFound = errors.New("pending operation not found")

// Pending 是一条在途操作（F-86）。
//
// Key**不是**自增主键：恢复方按业务 id 幂等（同一次等待重复通知是允许的），
// 由调用方给出稳定 id。
type Pending struct {
	ID         string
	SessionKey string
	// Kind 决定由谁来恢复，例如 "await" / "approval"。
	Kind string
	// Payload 是恢复方解释的 JSON。
	Payload   string
	CreatedAt int64
	ExpiresAt int64
	// Status 是状态机的当前值：pending / done / expired / orphaned。
	Status string
	// Note 记录状态迁移的原因（审计用）。
	Note string
}

// 在途状态取值。
const (
	PendingStatusPending  = "pending"
	PendingStatusDone     = "done"
	PendingStatusExpired  = "expired"
	PendingStatusOrphaned = "orphaned"
)

// UpsertPending 写入或更新一条在途记录。
func (s *Store) UpsertPending(ctx context.Context, p Pending) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("upsert pending: id must not be empty")
	}
	if strings.TrimSpace(p.SessionKey) == "" {
		return fmt.Errorf("upsert pending: session key must not be empty")
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = nowMillis()
	}
	if p.Status == "" {
		p.Status = PendingStatusPending
	}
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO pending (id, session_key, kind, payload, created_at, expires_at, status, note)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				session_key = excluded.session_key,
				kind        = excluded.kind,
				payload     = excluded.payload,
				expires_at  = excluded.expires_at,
				status      = excluded.status,
				note        = excluded.note
		`, p.ID, p.SessionKey, p.Kind, p.Payload, p.CreatedAt, p.ExpiresAt, p.Status, p.Note)
		if err != nil {
			return fmt.Errorf("upsert pending: %w", err)
		}
		return nil
	})
}

// CompletePending 把一条在途记录标记为结束（默认 done）。
//
// 记录不删除：状态迁移要可审计，而"谁在什么时候批准/超时"正是这类审计要回答的。
func (s *Store) CompletePending(ctx context.Context, id, status, note string) error {
	if status == "" {
		status = PendingStatusDone
	}
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE pending SET status = ?, note = ? WHERE id = ?`, status, note, id)
		if err != nil {
			return fmt.Errorf("complete pending: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrPendingNotFound
		}
		return nil
	})
}

// ListPending 返回指定状态的记录，按创建时间升序。
//
// status 为空时返回全部**未结束**（status = pending）的记录。
func (s *Store) ListPending(ctx context.Context, status string) ([]Pending, error) {
	if status == "" {
		status = PendingStatusPending
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_key, kind, payload, created_at, expires_at, status, note
		 FROM pending WHERE status = ? ORDER BY created_at ASC`, status)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Pending, 0, 8)
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.SessionKey, &p.Kind, &p.Payload,
			&p.CreatedAt, &p.ExpiresAt, &p.Status, &p.Note); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ExpirePending 把已过期的 pending 标记为过期，返回受影响的记录。
//
// 返回记录而不只是条数：调用方需要**逐条回灌**给原会话，
// 让用户知道"之前那件事没有被处理"，而不是让它悬空。
func (s *Store) ExpirePending(ctx context.Context, now int64) ([]Pending, error) {
	if now == 0 {
		now = nowMillis()
	}
	rows, err := s.ListPending(ctx, PendingStatusPending)
	if err != nil {
		return nil, err
	}
	var expired []Pending
	for _, p := range rows {
		if p.ExpiresAt <= 0 || p.ExpiresAt > now {
			continue
		}
		if err := s.CompletePending(ctx, p.ID, PendingStatusExpired, "到期未完成"); err != nil {
			if errors.Is(err, ErrPendingNotFound) {
				continue
			}
			return expired, err
		}
		p.Status = PendingStatusExpired
		p.Note = "到期未完成"
		expired = append(expired, p)
	}
	return expired, nil
}

// PrunePending 删除已结束且早于 cutoff 的记录，保证表有界。
func (s *Store) PrunePending(ctx context.Context, cutoff int64) (int, error) {
	var n int64
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM pending WHERE status <> ? AND created_at < ?`,
			PendingStatusPending, cutoff)
		if err != nil {
			return fmt.Errorf("prune pending: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// CountPending 返回未结束的记录数（观测用）。
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pending WHERE status = ?`, PendingStatusPending).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	return n, nil
}
