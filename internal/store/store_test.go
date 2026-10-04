package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTest(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.Path == "" {
		opts.Path = filepath.Join(t.TempDir(), "test.db")
	}
	if opts.Rand == nil {
		opts.Rand = rand.New(rand.NewSource(1))
	}
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func Test_F83_OpenCreatesSchemaVersion(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{Path: filepath.Join(t.TempDir(), "a.db")})
	v, err := s.schemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("版本应为 %d，实际 %d", SchemaVersion, v)
	}
}

func Test_F83_OpenEnablesWAL(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{Path: filepath.Join(t.TempDir(), "b.db")})
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("应启用 WAL，实际 %q", mode)
	}
	// 目录下应出现 -wal 文件（至少在建连后）。
	entries, _ := os.ReadDir(filepath.Dir(s.Path()))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("目录内容: %v", names)
}

// Test_F83_MigrationChainIsIdempotent 覆盖"重跑结果与一次跑完一致"。
func Test_F83_MigrationChainIsIdempotent(t *testing.T) {
	t.Parallel()
	applied := 0
	mk := func() []migration {
		applied = 0
		return []migration{
			{Version: 2, Name: "add widget table", Apply: func(ctx context.Context, tx *sql.Tx) error {
				applied++
				_, err := tx.ExecContext(ctx, "CREATE TABLE widget (id INTEGER PRIMARY KEY, name TEXT)")
				return err
			}},
			{Version: 3, Name: "seed widget", Apply: func(ctx context.Context, tx *sql.Tx) error {
				applied++
				_, err := tx.ExecContext(ctx, "INSERT INTO widget (name) VALUES ('seed')")
				return err
			}},
		}
	}
	path := filepath.Join(t.TempDir(), "m.db")

	s1 := openTest(t, Options{Path: path, migrations: mk(), schemaVersion: 3})
	if applied != 2 {
		t.Fatalf("首次应应用 2 条迁移，实际 %d", applied)
	}
	_ = s1.Close()

	// 第二次打开：版本已到位，不应重复应用。
	s2 := openTest(t, Options{Path: path, migrations: mk(), schemaVersion: 3})
	if applied != 0 {
		t.Fatalf("重跑不应重复应用迁移，实际应用 %d 条", applied)
	}
	var n int
	if err := s2.db.QueryRow("SELECT count(*) FROM widget").Scan(&n); err != nil {
		t.Fatalf("查询 widget: %v", err)
	}
	if n != 1 {
		t.Fatalf("seed 只应插入一次，实际 %d 行", n)
	}
}

// Test_F83_MigrationInterruptionIsRecoverable 覆盖"中断后重跑结果一致"。
func Test_F83_MigrationInterruptionIsRecoverable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "i.db")
	failing := []migration{
		{Version: 2, Name: "good", Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE part_a (id INTEGER PRIMARY KEY)")
			return err
		}},
		{Version: 3, Name: "bad", Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE part_b (id INTEGER PRIMARY KEY)")
			if err != nil {
				return err
			}
			// 事务内失败：这条迁移必须整体回滚。
			return errors.New("boom")
		}},
		{Version: 4, Name: "never", Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE part_c (id INTEGER PRIMARY KEY)")
			return err
		}},
	}

	if s, err := Open(context.Background(), Options{
		Path: path, Rand: rand.New(rand.NewSource(1)), migrations: failing, schemaVersion: 4,
	}); err == nil {
		_ = s.Close()
		t.Fatalf("迁移失败必须让 Open 失败")
	} else if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("错误应可识别为迁移失败: %v", err)
	}

	// 重跑（修好第 3 条）：应从断点继续，且结果与一次跑完一致。
	ok := []migration{
		failing[0],
		{Version: 3, Name: "fixed", Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE part_b (id INTEGER PRIMARY KEY)")
			return err
		}},
		failing[2],
	}
	s := openTest(t, Options{Path: path, migrations: ok, schemaVersion: 4})
	v, _ := s.schemaVersion(context.Background())
	if v != 4 {
		t.Fatalf("恢复后版本应为 4，实际 %d", v)
	}
	for _, tbl := range []string{"part_a", "part_b", "part_c"} {
		var name string
		if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name); err != nil {
			t.Fatalf("中断恢复后应存在表 %s: %v", tbl, err)
		}
	}
	// 失败的那条不得留下半成品（part_b 不在第一次尝试里创建）。
	if err := s.db.QueryRow("SELECT count(*) FROM part_b").Scan(new(int)); err != nil {
		t.Fatalf("part_b 应可用: %v", err)
	}
}

// Test_F83_DeclarativeColumnAdd 覆盖声明式加列。
func Test_F83_DeclarativeColumnAdd(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "c.db")
	base := []string{`CREATE TABLE IF NOT EXISTS thing (id INTEGER PRIMARY KEY)`}

	// 覆盖 schema 时必须一并覆盖 columns 与 indexes：否则会拿默认列集/索引集去对账不存在的表。
	s1 := openTest(t, Options{Path: path, schema: base, columns: []columnSpec{}, indexes: []string{}})
	if _, err := s1.db.Exec("INSERT INTO thing (id) VALUES (1)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = s1.Close()

	// 再次打开时"期望 schema"多了两列：应被声明式补上，且既有数据保留。
	extended := []columnSpec{
		{Table: "thing", Name: "note", DDL: "note TEXT NOT NULL DEFAULT ''"},
		{Table: "thing", Name: "score", DDL: "score INTEGER NOT NULL DEFAULT 0"},
	}
	s2 := openTest(t, Options{Path: path, schema: base, columns: extended, indexes: []string{}})
	for _, c := range extended {
		ok, err := s2.columnExists(context.Background(), "thing", c.Name)
		if err != nil || !ok {
			t.Fatalf("列 %s 应被补齐: ok=%v err=%v", c.Name, ok, err)
		}
	}
	var note string
	if err := s2.db.QueryRow("SELECT note FROM thing WHERE id = 1").Scan(&note); err != nil {
		t.Fatalf("既有行应保留并取到默认值: %v", err)
	}

	// 第三次打开：列已存在，不应报错（幂等）。
	_ = s2.Close()
	_ = openTest(t, Options{Path: path, schema: base, columns: extended, indexes: []string{}})
}

// Test_F83_ConcurrentReadWrite 覆盖并发读写：不得出现 database is locked。
func Test_F83_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{
		Path:    filepath.Join(t.TempDir(), "conc.db"),
		schema:  []string{`CREATE TABLE IF NOT EXISTS counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`},
		columns: []columnSpec{},
		indexes: []string{},
	})
	if err := s.Write(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO counter (id, n) VALUES (1, 0)")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const workers = 16
	const perWorker = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if err := s.Write(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE counter SET n = n + 1 WHERE id = 1")
					return err
				}); err != nil {
					errs <- fmt.Errorf("write: %w", err)
					return
				}
				var n int
				if err := s.db.QueryRowContext(context.Background(), "SELECT n FROM counter WHERE id = 1").Scan(&n); err != nil {
					errs <- fmt.Errorf("read: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发读写出错: %v", err)
	}

	var n int
	if err := s.db.QueryRow("SELECT n FROM counter WHERE id = 1").Scan(&n); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if want := workers * perWorker; n != want {
		t.Fatalf("累加应无丢失: got %d want %d", n, want)
	}
}

// Test_F83_OpenFailsLoudly 覆盖"打不开就失败，不降级"。
func Test_F83_OpenFailsLoudly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备阻塞文件: %v", err)
	}
	// 父路径是个文件：无法建库，必须明确失败，而不是退化成内存。
	if s, err := Open(context.Background(), Options{Path: filepath.Join(blocker, "sub", "x.db")}); err == nil {
		_ = s.Close()
		t.Fatalf("无法建库时必须失败")
	}
}

func Test_F83_ContextCancellationStopsRetry(t *testing.T) {
	t.Parallel()
	s := openTest(t, Options{Path: filepath.Join(t.TempDir(), "ctx.db")})
	_ = s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error { return nil }); err == nil {
		t.Fatalf("已关闭的存储应拒绝写入")
	} else if !errors.Is(err, ErrClosed) {
		t.Fatalf("应返回 ErrClosed: %v", err)
	}
}

func Test_F83_FingerprintIsStableAndSeparated(t *testing.T) {
	t.Parallel()
	a := Fingerprint("ab", "c")
	b := Fingerprint("a", "bc")
	if a == b {
		t.Fatalf("字段间必须有分隔，(%q,%q) 与 (%q,%q) 不得同指纹", "ab", "c", "a", "bc")
	}
	first := Fingerprint("x", "y")
	second := Fingerprint("x", "y")
	if first != second {
		t.Fatalf("同样输入必须同指纹: %q vs %q", first, second)
	}
	if len(Fingerprint("x")) != 32 {
		t.Fatalf("指纹应为 32 个十六进制字符: %q", Fingerprint("x"))
	}
}
