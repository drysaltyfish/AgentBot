package toggle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// MemoryStore 是基于 map 的 Store，仅用于测试与进程内临时运行。
type MemoryStore struct {
	mu sync.RWMutex
	m  map[Key]bool
}

// NewMemoryStore 构造空的内存 Store。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: make(map[Key]bool)}
}

// Get 实现 Store。
func (s *MemoryStore) Get(k Key) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	on, ok := s.m[k]
	return on, ok
}

// Set 实现 Store。
func (s *MemoryStore) Set(k Key, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[Key]bool)
	}
	s.m[k] = on
	return nil
}

// Delete 实现 Store。
func (s *MemoryStore) Delete(k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

// All 实现 Store，返回按键排序的键快照。
func (s *MemoryStore) All() ([]Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Key, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sortKeys(out)
	return out, nil
}

// fileEntry 是 FileStore 的 JSON 表示单元。
type fileEntry struct {
	Plugin  string `json:"plugin"`
	GroupID int64  `json:"group_id"`
	On      bool   `json:"on"`
}

// FileStore 把开关状态持久化为 JSON 文件：打开时载入已有内容，
// 每次写入采用“临时文件 + rename”原子替换，保证重启后状态不丢。
type FileStore struct {
	path string

	mu sync.RWMutex
	m  map[Key]bool
}

// NewFileStore 打开（或创建）指定路径的文件 Store。文件不存在时返回空
// Store；内容非法时返回 error。path 不能为空。
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("toggle: empty file store path")
	}
	s := &FileStore{path: path, m: make(map[Key]bool)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("toggle: read store %q: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}
	var entries []fileEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("toggle: decode store %q: %w", path, err)
	}
	for _, e := range entries {
		s.m[Key{Plugin: e.Plugin, GroupID: e.GroupID}] = e.On
	}
	return s, nil
}

// Get 实现 Store。
func (s *FileStore) Get(k Key) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	on, ok := s.m[k]
	return on, ok
}

// Set 实现 Store。落盘失败时回滚内存状态，保持内存与磁盘一致。
func (s *FileStore) Set(k Key, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.m[k]
	s.m[k] = on
	if err := s.flushLocked(); err != nil {
		if had {
			s.m[k] = prev
		} else {
			delete(s.m, k)
		}
		return err
	}
	return nil
}

// Delete 实现 Store。落盘失败时回滚内存状态。
func (s *FileStore) Delete(k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.m[k]
	if !had {
		return nil
	}
	delete(s.m, k)
	if err := s.flushLocked(); err != nil {
		s.m[k] = prev
		return err
	}
	return nil
}

// All 实现 Store，返回按键排序的键快照。
func (s *FileStore) All() ([]Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Key, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sortKeys(out)
	return out, nil
}

// flushLocked 在持锁状态下原子写回整个文件。
func (s *FileStore) flushLocked() error {
	entries := make([]fileEntry, 0, len(s.m))
	for k, on := range s.m {
		entries = append(entries, fileEntry{Plugin: k.Plugin, GroupID: k.GroupID, On: on})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Plugin != entries[j].Plugin {
			return entries[i].Plugin < entries[j].Plugin
		}
		return entries[i].GroupID < entries[j].GroupID
	})
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("toggle: encode store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("toggle: create temp for %q: %w", s.path, err)
	}
	tmpName := tmp.Name()
	// rename 成功后该 defer 无害地失败；失败路径下则负责清理临时文件。
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("toggle: write temp for %q: %w", s.path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("toggle: sync temp for %q: %w", s.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("toggle: close temp for %q: %w", s.path, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("toggle: rename temp to %q: %w", s.path, err)
	}
	return nil
}

// sortKeys 按键的 (插件名, 群号) 升序排序。
func sortKeys(keys []Key) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Plugin != keys[j].Plugin {
			return keys[i].Plugin < keys[j].Plugin
		}
		return keys[i].GroupID < keys[j].GroupID
	})
}

// 编译期断言：两种 Store 实现必须始终满足接口（F-79）。
var (
	_ Store = (*MemoryStore)(nil)
	_ Store = (*FileStore)(nil)
)
