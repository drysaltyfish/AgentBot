package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SchemaVersion 是当前期望的 schema 版本。
//
// 它只在**需要动数据或动索引**时递增。纯加列由 reconcileColumns 声明式处理，
// 不占版本号——否则迁移链会被大量无意义的版本号淹没，真正需要人工确认的那几条
// 反而淹没在噪声里。
const SchemaVersion = 1

var (
	// ErrMigrationFailed 表示迁移失败。携带失败时的版本，便于人工判断停在哪儿。
	ErrMigrationFailed = errors.New("schema migration failed")
)

// migration 是一条版本门控迁移。
type migration struct {
	Version int
	Name    string
	Apply   func(ctx context.Context, tx *sql.Tx) error
}

// schemaSQL 是"期望的 schema"，全部语句必须幂等（IF NOT EXISTS）。
//
// 新表、新索引、新触发器都放这里；它们天然可重复执行。
var schemaSQL = append([]string{
	`CREATE TABLE IF NOT EXISTS schema_version (
		id      INTEGER PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
}, append(append(append(append(messagesSchema, sessionsSchema...), memoriesSchema...), pendingSchema...), promptSnapshotSchema...)...)

// columnSpec 描述一个声明式维护的列。
type columnSpec struct {
	Table string
	Name  string
	DDL   string // 形如 "created_at INTEGER NOT NULL DEFAULT 0"
}

// desiredColumns 是"表应当具备的列"。缺失的会被 ADD COLUMN 补齐。
//
// 只用于**加列**。改类型、改约束、改索引都不在这里做——那些必须进 migrations，
// 因为 ALTER TABLE 表达不了，需要重建表或回填数据。
var desiredColumns = append([]columnSpec{
	// 各功能表由后续 ticket 追加（F-85 台账、F-87 记忆、F-86 在途、F-89 快照）。
}, append(append(append(messagesColumns, sessionsColumns...), memoriesColumns...), promptSnapshotColumns...)...)

// migrations 是版本门控链，按版本升序。
//
// v1 是初始版本，没有历史数据要动，因此为空——但机制必须是通的，
// 否则等到真正需要迁移时才发现它没被验证过。
var migrations []migration

// migrate 把数据库推进到 SchemaVersion。
//
// 顺序：版本门控链 -> 幂等建表 -> 声明式加列 -> 落版本号。
// 每一步都可重复执行：中途失败后重跑，结果与一次跑完一致。
func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureVersionTable(ctx); err != nil {
		return err
	}
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}

	for _, m := range s.migrations() {
		if m.Version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}

	// 建表与加列放在事务里：要么整批生效，要么完全不动。
	if err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range s.schema() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("apply schema: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := s.reconcileColumns(ctx); err != nil {
		return err
	}

	return s.setSchemaVersion(ctx, s.targetVersion())
}

func (s *Store) schema() []string {
	if s.opts.schema != nil {
		return s.opts.schema
	}
	return schemaSQL
}

func (s *Store) migrations() []migration {
	if s.opts.migrations != nil {
		return s.opts.migrations
	}
	return migrations
}

func (s *Store) targetVersion() int {
	if s.opts.schemaVersion > 0 {
		return s.opts.schemaVersion
	}
	return SchemaVersion
}

func (s *Store) ensureVersionTable(ctx context.Context) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`)
		if err != nil {
			return fmt.Errorf("create schema_version: %w", err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO schema_version (id, version, updated_at) VALUES (1, 0, ?)
			 ON CONFLICT(id) DO NOTHING`, nowMillis())
		return err
	})
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version WHERE id = 1`).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	return v, nil
}

func (s *Store) setSchemaVersion(ctx context.Context, v int) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE schema_version SET version = ?, updated_at = ? WHERE id = 1`, v, nowMillis())
		if err != nil {
			return fmt.Errorf("set schema_version: %w", err)
		}
		return nil
	})
}

// applyMigration 在单个事务里执行一条迁移并落版本号。
//
// 同一事务保证"改了数据但没记版本"这种半完成状态不会出现。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := m.Apply(ctx, tx); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_version SET version = ?, updated_at = ? WHERE id = 1`, m.Version, nowMillis()); err != nil {
			return fmt.Errorf("record version %d: %w", m.Version, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: version %d (%s): %w", ErrMigrationFailed, m.Version, m.Name, err)
	}
	return nil
}

// reconcileColumns 补齐缺失的列（幂等）。
func (s *Store) reconcileColumns(ctx context.Context) error {
	for _, spec := range s.columns() {
		exists, err := s.columnExists(ctx, spec.Table, spec.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		// 表名与列定义都来自本包常量，不含外部输入；SQL 也不支持把标识符参数化。
		//nolint:gosec // G202: 拼接的是代码内常量，非用户输入
		stmt := "ALTER TABLE " + quoteIdent(spec.Table) + " ADD COLUMN " + spec.DDL
		if err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("add column %s.%s: %w", spec.Table, spec.Name, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) columns() []columnSpec {
	if s.opts.columns != nil {
		return s.opts.columns
	}
	return desiredColumns
}

func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return false, fmt.Errorf("read table_info %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var (
		cid     int
		name    string
		ctype   string
		notNull int
		dflt    sql.NullString
		pk      int
	)
	if err := rows.Err(); err != nil {
		return false, err
	}
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan table_info %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate table_info %s: %w", table, err)
	}
	return false, nil
}

// quoteIdent 给标识符加双引号。表名来自代码常量，这里的引号只是防御。
func quoteIdent(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		if r == '"' {
			out = append(out, '"')
		}
		out = append(out, r)
	}
	out = append(out, '"')
	return string(out)
}
