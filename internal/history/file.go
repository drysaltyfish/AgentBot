package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File 是 JSONL 追加型历史存储，便于直接查看与排查（F-38 内置实现之一）。
type File struct {
	mu       sync.Mutex
	path     string
	maxItems int
	trimmer  Trimmer
	now      func() time.Time
}

type fileRecord struct {
	Key  string `json:"key"`
	Item Item   `json:"item"`
}

// NewFile 构造 JSONL 历史；maxItems <= 0 时使用 DefaultMax。
func NewFile(path string, maxItems int) *File {
	if maxItems <= 0 {
		maxItems = DefaultMax
	}
	return &File{path: path, maxItems: maxItems, trimmer: Window{N: maxItems}, now: time.Now}
}

// WithTrimmer 替换裁剪策略（例如 HighWater，让裁剪批量发生而不是每次追加都裁）。
func (f *File) WithTrimmer(t Trimmer) *File {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t != nil {
		f.trimmer = t
	}
	return f
}

// Append 追加一行 JSON；超过上限时按策略裁剪并重写文件。
func (f *File) Append(ctx context.Context, key string, item Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.At.IsZero() {
		item.At = f.now()
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(f.path), 0o750); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	line, err := json.Marshal(fileRecord{Key: key, Item: item})
	if err != nil {
		_ = fh.Close()
		return fmt.Errorf("encode history record: %w", err)
	}
	_, werr := fh.Write(append(line, '\n'))
	cerr := fh.Close()
	if werr != nil {
		return fmt.Errorf("write history record: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("close history file: %w", cerr)
	}

	if n := f.countLocked(key); n > f.maxItems {
		return f.rewriteLocked(key, f.trimmer.Apply)
	}
	return nil
}

// Messages 读取某个 key 的全部条目（返回副本；为空时返回空切片）。
func (f *File) Messages(ctx context.Context, key string) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	items, err := f.readLocked(key)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		out = append(out, it.Clone())
	}
	return out, nil
}

// Reset 删除某个 key 的全部条目。
func (f *File) Reset(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rewriteLocked(key, func([]Item) []Item { return []Item{} })
}

// Trim 只保留某个 key 的最近 n 条。
func (f *File) Trim(ctx context.Context, key string, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rewriteLocked(key, func(items []Item) []Item { return Window{N: n}.Apply(items) })
}

func (f *File) readLocked(key string) ([]Item, error) {
	all, err := f.readAllLocked()
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, rec := range all {
		if rec.Key == key {
			out = append(out, rec.Item)
		}
	}
	return out, nil
}

func (f *File) countLocked(key string) int {
	items, err := f.readLocked(key)
	if err != nil {
		return 0
	}
	return len(items)
}

// rewriteLocked 用"读全部 → 重排目标 key → 原子替换"的方式改写文件。
func (f *File) rewriteLocked(key string, transform func([]Item) []Item) error {
	all, err := f.readAllLocked()
	if err != nil {
		return err
	}
	var target []Item
	for _, rec := range all {
		if rec.Key == key {
			target = append(target, rec.Item)
		}
	}
	kept := transform(target)

	tmp := f.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open temp history file: %w", err)
	}
	w := bufio.NewWriter(fh)
	emitted := 0
	failed := false
	for _, rec := range all {
		if rec.Key != key {
			if err := writeRecord(w, rec); err != nil {
				failed = true
				break
			}
			continue
		}
		if emitted >= len(kept) {
			continue
		}
		if err := writeRecord(w, fileRecord{Key: key, Item: kept[emitted]}); err != nil {
			failed = true
			break
		}
		emitted++
	}
	if !failed {
		if err := w.Flush(); err != nil {
			failed = true
		}
	}
	if cerr := fh.Close(); cerr != nil && !failed {
		failed = true
	}
	if failed {
		_ = os.Remove(tmp)
		return fmt.Errorf("rewrite history file for key %q failed", key)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("replace history file: %w", err)
	}
	return nil
}

func (f *File) readAllLocked() ([]fileRecord, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open history file: %w", err)
	}
	defer func() { _ = fh.Close() }()

	var out []fileRecord
	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec fileRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// 单行损坏不应让整段历史不可读。
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan history file: %w", err)
	}
	return out, nil
}

func writeRecord(w *bufio.Writer, rec fileRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode history record: %w", err)
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write history record: %w", err)
	}
	return nil
}

var _ History = (*File)(nil)
