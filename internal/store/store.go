// Package store 是 AgentBot 的唯一持久层（F-83）。
//
// 选型与分层理由见 docs/adr/0003。这里只强调三条纪律：
//
//  1. **单写者**：进程内所有写事务经 Store.Write 串行化。SQLite 只支持一个写者，
//     与其让并发写者在库层面撞锁，不如在应用层排队，把"锁竞争"变成"等待"。
//  2. **短超时 + 抖动重试**：busy_timeout 取短值（默认 1s）。默认的长超时会让多个
//     写者按同一节奏退避，形成护航效应（convoy effect）；短超时 + 随机抖动让它们错开。
//  3. **失败即失败**：数据库打不开就启动失败。不要静默降级成内存——那会悄悄丢数据，
//     而"悄悄"正是这类问题的全部危害。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// DefaultBusyTimeout 是 SQLite 的忙等超时。
	//
	// 刻意取**短值**：见包注释的护航效应说明。
	DefaultBusyTimeout = time.Second

	// driverName 是驱动注册名。
	driverName = "sqlite"

	writeRetryMax   = 15
	checkpointEvery = 50
)

var (
	// ErrClosed 表示存储已关闭。
	ErrClosed = errors.New("store is closed")
	// ErrBusy 表示经过全部重试后仍未能获得写锁。
	ErrBusy = errors.New("store is busy after retries")
)

// retryDelay 是第 attempt 次重试前的等待时长（含抖动）。
//
// 抖动是关键：确定性退避会让所有写者同时醒来再撞一次。
func retryDelay(attempt int, rnd *rand.Rand) time.Duration {
	const minDelay = 20 * time.Millisecond
	const maxDelay = 150 * time.Millisecond
	d := minDelay + time.Duration(rnd.Int63n(int64(maxDelay-minDelay)+1))
	// 轻微递增，避免长尾反复撞在同一窗口。
	if attempt > 4 {
		d += time.Duration(attempt-4) * 10 * time.Millisecond
	}
	if d > maxDelay*2 {
		d = maxDelay * 2
	}
	return d
}

// Store 是持久层句柄。
type Store struct {
	db   *sql.DB
	path string
	opts Options

	// writeMu 实现单写者纪律：写事务在这里排队。
	writeMu sync.Mutex

	mu     sync.Mutex
	writes int
	closed bool
	rnd    *rand.Rand
}

// Options 是打开存储的参数。
type Options struct {
	// Path 是数据库文件路径。为空时用 DefaultPath。
	Path string
	// BusyTimeout <= 0 时用 DefaultBusyTimeout。
	BusyTimeout time.Duration
	// Rand 可注入确定性随机源，便于测试抖动路径。
	Rand *rand.Rand
	// Warn 接收降级路径的告警（例如 FTS 失败后回退 LIKE）。
	// 降级可以发生，但必须留下痕迹——静默降级是这类问题最难查的形态。
	Warn func(msg string)

	// 以下字段仅供同包测试覆盖，外部调用方不应设置。
	schema        []string
	migrations    []migration
	columns       []columnSpec
	schemaVersion int
}

// DefaultPath 是默认数据库路径。
func DefaultPath() string { return filepath.Join("data", "agentbot.db") }

// Open 打开（必要时创建）数据库并完成迁移。
//
// 任何一步失败都返回错误——调用方必须让启动失败，不得降级。
func Open(ctx context.Context, opts Options) (*Store, error) {
	path := opts.Path
	if path == "" {
		path = DefaultPath()
	}
	timeout := opts.BusyTimeout
	if timeout <= 0 {
		timeout = DefaultBusyTimeout
	}
	rnd := opts.Rand
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	// DSN 里带上 pragma 与事务模式：每次建连都生效，不依赖"打开后记得设一下"。
	//   _txlock=immediate 让写事务在 BEGIN 时就取锁，锁竞争在事务开始暴露，
	//   而不是执行到一半才失败——后者会浪费掉前面所有的读。
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=synchronous(NORMAL)"+
			"&_pragma=foreign_keys(1)&_txlock=immediate",
		filepath.ToSlash(path), timeout.Milliseconds(),
	)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// WAL 允许"多读 + 单写"。写由 writeMu 串行化，读可以并发，因此池子不必压到 1。
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, path: path, rnd: rnd, opts: opts}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database %s: %w", path, err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path 返回数据库文件路径。
// Ping 检查持久层是否可用（F-69 的就绪探针）。
//
// 用一次最廉价的查询而不是建新连接：SQLite 是嵌入式单文件，"能查"就是"可用"。
func (s *Store) Ping(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("store ping: %w", err)
	}
	return nil
}

func (s *Store) Path() string { return s.path }

// Close 关闭存储。
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.db.Close()
}

// Write 在一个写事务里执行 fn，带单写者串行化与抖动重试。
//
// fn 可能被**重复调用**（重试时），因此它只能是纯 SQL 操作、不得有外部副作用。
// 失败时事务回滚，所以重复执行是安全的——但这条约束必须由调用方遵守。
func (s *Store) Write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}

	var lastErr error
	for attempt := 0; attempt <= writeRetryMax; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryDelay(attempt, s.rnd)); err != nil {
				return err
			}
		}
		err := s.writeOnce(ctx, fn)
		if err == nil {
			s.afterWrite(ctx)
			return nil
		}
		lastErr = err
		if !isBusy(err) {
			return err
		}
	}
	return fmt.Errorf("%w: %w", ErrBusy, lastErr)
}

func (s *Store) writeOnce(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Read 在共享连接池上执行一次只读操作，供同进程内的其他持久化实现复用连接与配置。
//
// 暴露 *sql.DB 而不是逐域封装方法：表结构属于各自的领域包（例如分层记忆表），
// 而连接池、WAL、busy_timeout 与单写者纪律属于本包。
//
// 调用方**只读**：写必须走 Write，否则单写者纪律会被绕过，
// 那正是 F-83 要避免的"轮到执行到一半才发现锁冲突"。
func (s *Store) Read(ctx context.Context, fn func(ctx context.Context, db *sql.DB) error) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return fn(ctx, s.db)
}

// afterWrite 计数并在够次数时做一次 PASSIVE checkpoint，避免 WAL 无限增长。
func (s *Store) afterWrite(ctx context.Context) {
	s.mu.Lock()
	s.writes++
	n := s.writes
	s.mu.Unlock()
	if n%checkpointEvery != 0 {
		return
	}
	// PASSIVE 不阻塞读者，也不强制截断；只是把已提交页刷回主库。
	// 用调用方的 ctx：checkpoint 不该比触发它的那次写活得更久。
	_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
}

// isBusy 判断错误是否为"数据库忙"。
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"database is locked", "database table is locked", "SQLITE_BUSY", "database is busy",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// warn 调用告警钩子（未配置时丢弃）。
func (s *Store) warn(msg string) {
	if s.opts.Warn != nil {
		s.opts.Warn(msg)
	}
}

// nowMillis 返回 Unix 毫秒。
//
// 时间一律存整数毫秒：本地时区与夏令时在数据库里没有意义，只会让比较出岔子。
func nowMillis() int64 { return time.Now().UnixMilli() }

// sleepCtx 睡眠 d，或在 ctx 取消时提前返回。
//
// 不用 time.Sleep：等待必须可被取消，否则关闭流程会被一次重试拖住。
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ErrImportSourceMissing 表示导入源文件不存在。
var ErrImportSourceMissing = errors.New("import source does not exist")
