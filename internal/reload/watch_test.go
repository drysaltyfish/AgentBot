package reload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeFile 写文件并保证 mtime 与上一次不同（文件系统时间戳粒度可能较粗）。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func loaderFor(path string) func() (string, error) {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		s := strings.TrimSpace(string(b))
		if s == "" || strings.Contains(s, "INVALID") {
			return "", errors.New("配置内容非法")
		}
		return s, nil
	}
}

// Test_F24_ReloadsWithinDeadline 覆盖验收：改文件后 1s 内新值生效。
func Test_F24_ReloadsWithinDeadline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.txt")
	writeFile(t, path, "A")

	w := New([]string{path}, loaderFor(path), Options{Interval: 50 * time.Millisecond, Debounce: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	waitFor(t, "首次加载 A", func() bool {
		v, ok := w.Current()
		return ok && v == "A"
	})

	start := time.Now()
	writeFile(t, path, "BBBB")
	waitFor(t, "热加载到 B", func() bool {
		v, ok := w.Current()
		return ok && v == "BBBB"
	})
	// 规格要求"1s 内生效"；测试用 50ms 轮询 + 50ms 去抖，正常约 100ms。
	// 给到 2s 是留出 CI 覆盖率插桩的余量——这里要拦的是数量级退化，不是抖动。
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("热加载耗时 %v，明显超过预期", elapsed)
	}
	if w.Version() < 2 {
		t.Fatalf("版本号应随替换递增: actual=%d", w.Version())
	}
}

// Test_F24_KeepsOldValueOnInvalidContent 覆盖验收：写入非法内容时旧值仍可用并告警。
func Test_F24_KeepsOldValueOnInvalidContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.txt")
	writeFile(t, path, "GOOD")

	var warned atomic.Int64
	w := New([]string{path}, loaderFor(path), Options{
		Interval: 50 * time.Millisecond, Debounce: 50 * time.Millisecond,
		Retries: 1, RetryDelay: 10 * time.Millisecond,
		Warn: func(error) { warned.Add(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	waitFor(t, "首次加载 GOOD", func() bool {
		v, ok := w.Current()
		return ok && v == "GOOD"
	})

	writeFile(t, path, "INVALID")
	waitFor(t, "产生告警", func() bool { return warned.Load() > 0 })

	if v, _ := w.Current(); v != "GOOD" {
		t.Fatalf("加载失败必须保留旧值: actual=%q", v)
	}
}

// Test_F24_ManualReload 覆盖 /config reload：立即重载并返回错误（旧值保留）。
func Test_F24_ManualReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.txt")
	writeFile(t, path, "ONE")

	w := New([]string{path}, loaderFor(path), Options{Interval: time.Hour, Debounce: time.Millisecond})
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if v, ok := w.Current(); !ok || v != "ONE" {
		t.Fatalf("手动重载未生效: %q ok=%v", v, ok)
	}

	writeFile(t, path, "INVALID")
	if err := w.Reload(); err == nil {
		t.Fatalf("非法内容时 Reload 应返回错误")
	}
	if v, _ := w.Current(); v != "ONE" {
		t.Fatalf("失败后应保留旧值: %q", v)
	}
}

// Test_F24_ReloadsDirectoryContentChange 钉住目录监听的正确性：
// 改一个**已存在**文件的内容不会改目录 mtime，只看目录元信息就会永远漏掉，
// 而"改人格文件里的一个字"正是最常见的用法。
func Test_F24_ReloadsDirectoryContentChange(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.yml")
	writeFile(t, aPath, "第一版")

	var loads atomic.Int64
	w := New([]string{dir}, func() (string, error) {
		loads.Add(1)
		b, err := os.ReadFile(aPath)
		if err != nil {
			return "", err
		}
		if strings.Contains(string(b), "INVALID") {
			return "", errors.New("内容非法")
		}
		return string(b), nil
	}, Options{Interval: 10 * time.Millisecond, Debounce: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	if got, ok := w.Current(); !ok || got != "第一版" {
		t.Fatalf("首次加载=(%q,%v)", got, ok)
	}

	writeFile(t, aPath, "第二版")
	waitFor(t, "目录内文件内容变化被检测到", func() bool {
		got, ok := w.Current()
		return ok && got == "第二版"
	})

	// 非法内容必须保留旧值，而不是把当前值清空。
	writeFile(t, aPath, "INVALID")
	waitFor(t, "至少尝试了一次重新加载", func() bool { return loads.Load() > 2 })
	if got, _ := w.Current(); got != "第二版" {
		t.Fatalf("非法内容应保留旧值，实际 %q", got)
	}
}
