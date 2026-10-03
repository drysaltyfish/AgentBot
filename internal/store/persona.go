package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GetPersona 读取某会话已持久化的人格（F-82）。
//
// ok=false 表示**从未设置过**，与"设置成空"是两回事：调用方据此回退到配置
// 里的默认人格，而不是把空串当成一次有效的显式选择。
func (s *Store) GetPersona(ctx context.Context, sessionKey string) (string, bool, error) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return "", false, nil
	}
	var persona string
	err := s.db.QueryRowContext(ctx,
		`SELECT persona FROM session_personas WHERE session_key = ?`, key).Scan(&persona)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read persona: %w", err)
	}
	return persona, true, nil
}

// SetPersona 持久化某会话的人格（F-82）。
//
// 空人格与空会话键都被拒绝：静默写空等价于"下次启动时人格被悄悄重置"，
// 而人格是用户显式设定的状态，不该有这种失败模式。
func (s *Store) SetPersona(ctx context.Context, sessionKey, persona string) error {
	key := strings.TrimSpace(sessionKey)
	name := strings.TrimSpace(persona)
	if key == "" {
		return fmt.Errorf("store: session key is empty")
	}
	if name == "" {
		return fmt.Errorf("store: persona is empty")
	}
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO session_personas (session_key, persona, updated_at)
			 VALUES (?, ?, ?)
			 ON CONFLICT(session_key) DO UPDATE SET
			   persona = excluded.persona,
			   updated_at = excluded.updated_at`,
			key, name, nowMillis())
		if err != nil {
			return fmt.Errorf("write persona: %w", err)
		}
		return nil
	})
}

// DeletePersona 清除某会话的人格（回到配置默认）。
//
// 不存在时返回 false：调用方需要区分"确实清掉了一条"与"本来就没有"。
func (s *Store) DeletePersona(ctx context.Context, sessionKey string) (bool, error) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return false, nil
	}
	var affected int64
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM session_personas WHERE session_key = ?`, key)
		if err != nil {
			return fmt.Errorf("delete persona: %w", err)
		}
		affected, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete persona rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}
