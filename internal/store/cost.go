package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SaveCostSnapshot 覆盖保存成本聚合快照（F-66）。
//
// data 是调用方序列化后的字节：本包不认识 cost.Snapshot 的形状，
// 也不该认识——存储层只负责"把字节安全地放进去、完整地取出来"。
func (s *Store) SaveCostSnapshot(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("store: cost snapshot is empty")
	}
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO cost_snapshot (id, data, updated_at) VALUES (1, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			data, nowMillis())
		if err != nil {
			return fmt.Errorf("write cost snapshot: %w", err)
		}
		return nil
	})
}

// LoadCostSnapshot 读取成本聚合快照；从未保存过时返回 (nil, false, nil)。
func (s *Store) LoadCostSnapshot(ctx context.Context) ([]byte, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM cost_snapshot WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read cost snapshot: %w", err)
	}
	return data, true, nil
}
