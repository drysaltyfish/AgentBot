// Package reload 提供配置/资产的热加载：监听文件变化 → 去抖 → 重新加载 → 原子替换（F-24）。
//
// 关于 fsnotify：规格提到用 fsnotify，但仓库的依赖政策（FEATURES §0.4）是
// "能用标准库实现的能力不引入依赖"，而热加载只需要"文件变了就重载"——
// 轮询 mtime/size 就够，且没有平台差异与 CGO 负担。因此这里用标准库实现，
// 并把监听方式藏在 Watcher 内部，将来要换 fsnotify 不影响接口。
package reload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Options 配置监听行为。
type Options struct {
	// Interval 是轮询间隔；<=0 取 500ms（配合 300ms 去抖，1s 内生效）。
	Interval time.Duration
	// Debounce 是变更去抖时长；<=0 取 300ms，避免编辑器分多次写入触发多轮加载。
	Debounce time.Duration
	// Retries 是加载失败的重试次数；<=0 取 3（应对"写了一半的文件"）。
	Retries int
	// RetryDelay 是重试间隔；<=0 取 100ms。
	RetryDelay time.Duration
	// Warn 接收失败告警（加载失败时旧值保留，绝不置空）。
	Warn func(error)
	// OnSwap 在成功替换后回调，参数是版本号与来源指纹。
	OnSwap func(version uint64, fingerprint string)
}

// Watcher 监听一组文件，按需重新加载并原子替换当前值。
//
// 泛型是为了让调用方决定"加载出什么"：整个 Config、提示词、权限表都可以。
type Watcher[T any] struct {
	paths []string
	load  func() (T, error)
	opts  Options

	cur     atomic.Pointer[T]
	version atomic.Uint64
	stop    chan struct{}
}

// New 构造监听器；paths 为空时 Start 不会做任何事（视为未启用）。
func New[T any](paths []string, load func() (T, error), opts Options) *Watcher[T] {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 300 * time.Millisecond
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 100 * time.Millisecond
	}
	return &Watcher[T]{paths: paths, load: load, opts: opts, stop: make(chan struct{})}
}

// Start 起监听 goroutine；ctx 结束或 Stop 被调用时退出。
//
// 启动时会立刻加载一次：监听器不该让调用方额外写一遍"先加载"。
func (w *Watcher[T]) Start(ctx context.Context) {
	if w == nil || len(w.paths) == 0 {
		return
	}
	// 先同步取一次指纹再起循环：否则"写入发生在 loop 取基准之前"会被漏掉——
	// 循环看到的已经是新状态，永远检测不到变化（CI 上就复现了这个竞态）。
	last := w.fingerprint()
	w.tryLoad()
	go w.loop(ctx, last)
}

// Stop 停止监听（幂等）。
func (w *Watcher[T]) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

func (w *Watcher[T]) loop(ctx context.Context, last string) {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	var pendingSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case now := <-ticker.C:
			fp := w.fingerprint()
			if fp != last {
				last = fp
				pendingSince = now
				continue
			}
			if !pendingSince.IsZero() && now.Sub(pendingSince) >= w.opts.Debounce {
				pendingSince = time.Time{}
				w.tryLoad()
			}
		}
	}
}

// Current 返回当前生效的值；从未加载成功时返回 false。
func (w *Watcher[T]) Current() (T, bool) {
	var zero T
	if w == nil {
		return zero, false
	}
	p := w.cur.Load()
	if p == nil {
		return zero, false
	}
	return *p, true
}

// Version 返回成功替换的次数（0 表示还没加载过）。
func (w *Watcher[T]) Version() uint64 {
	if w == nil {
		return 0
	}
	return w.version.Load()
}

// Fingerprint 返回当前文件指纹，供管理命令展示"这一版是从哪来的"。
func (w *Watcher[T]) Fingerprint() string {
	if w == nil {
		return ""
	}
	return w.fingerprint()
}

// Reload 立即重新加载一次（/config reload 用它）；失败时保留旧值。
func (w *Watcher[T]) Reload() error {
	return w.tryLoadErr()
}

func (w *Watcher[T]) tryLoad() {
	_ = w.tryLoadErr()
}

func (w *Watcher[T]) tryLoadErr() error {
	var (
		v   T
		err error
	)
	for attempt := 0; attempt <= w.opts.Retries; attempt++ {
		v, err = w.load()
		if err == nil {
			break
		}
		if attempt == w.opts.Retries {
			break
		}
		if !w.waitRetry() {
			return err
		}
	}
	if err != nil {
		// 加载失败：保留旧值，绝不置空（清空配置比用旧配置危险得多）。
		if w.opts.Warn != nil {
			w.opts.Warn(fmt.Errorf("reload failed, keeping the previous value: %w", err))
		}
		return err
	}
	w.cur.Store(&v)
	version := w.version.Add(1)
	if w.opts.OnSwap != nil {
		w.opts.OnSwap(version, w.fingerprint())
	}
	return nil
}

// waitRetry 等待一次重试间隔；Stop 时提前返回 false。
func (w *Watcher[T]) waitRetry() bool {
	timer := time.NewTimer(w.opts.RetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-w.stop:
		return false
	}
}

// fingerprint 汇总被监听文件的"身份"：mtime + 大小；缺失标记为 missing。
//
// 小文件额外算内容摘要：文件系统时间戳粒度可能很粗（CI 容器里尤其如此），
// 而"把 A 改成 B"这种同长度的改动在只看 mtime+size 时会漏掉——那是致命的，
// 因为配置改一个字正是最常见的场景。
func (w *Watcher[T]) fingerprint() string {
	var b strings.Builder
	for _, p := range w.paths {
		info, err := os.Stat(p)
		if err != nil {
			b.WriteString(p + "=missing;")
			continue
		}
		fmt.Fprintf(&b, "%s=%d:%d", p, info.ModTime().UnixNano(), info.Size())
		if info.Size() <= maxDigestBytes {
			if sum, derr := fileDigest(p); derr == nil {
				b.WriteString(":" + sum)
			}
		}
		b.WriteString(";")
	}
	return b.String()
}

// maxDigestBytes 是"直接算内容摘要"的文件大小上限（配置与提示词都远小于它）。
const maxDigestBytes = 1 << 16

// fileDigest 返回文件内容的短摘要；读取失败时返回错误（调用方退回只看元信息）。
func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}
