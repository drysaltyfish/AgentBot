package history

import (
	"context"
	"sync"
	"time"
)

// DefaultMax 是每个 key 默认保留的条目上限。
const DefaultMax = 50

// Memory 是进程内历史存储。
type Memory struct {
	mu      sync.Mutex
	byKey   map[string][]Item
	max     int
	trimmer Trimmer
	now     func() time.Time
}

// NewMemory 构造内存历史；max <= 0 时使用 DefaultMax。
func NewMemory(max int) *Memory {
	if max <= 0 {
		max = DefaultMax
	}
	return &Memory{
		byKey:   map[string][]Item{},
		max:     max,
		trimmer: Window{N: max},
		now:     time.Now,
	}
}

// WithTrimmer 替换裁剪策略（例如 M3 的 TokenBudget）。
func (m *Memory) WithTrimmer(t Trimmer) *Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t != nil {
		m.trimmer = t
	}
	return m
}

// WithClock 注入时间源（测试用）。
func (m *Memory) WithClock(now func() time.Time) *Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now != nil {
		m.now = now
	}
	return m
}

// Append 追加一条条目，并按上限裁剪。
func (m *Memory) Append(ctx context.Context, key string, item Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.At.IsZero() {
		item.At = m.now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byKey[key] = append(m.byKey[key], item)
	if len(m.byKey[key]) > m.max {
		m.byKey[key] = m.trimmer.Apply(m.byKey[key])
	}
	return nil
}

// Messages 返回条目副本；历史为空时返回空切片而不是 nil。
func (m *Memory) Messages(ctx context.Context, key string) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.byKey[key]
	out := make([]Item, 0, len(src))
	for _, it := range src {
		out = append(out, it.Clone())
	}
	return out, nil
}

// Reset 清空某个 key。
func (m *Memory) Reset(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byKey, key)
	return nil
}

// Trim 只保留最近 n 条（仍保证 tool 调用与结果配对完整）。
func (m *Memory) Trim(ctx context.Context, key string, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.byKey[key]
	if len(cur) == 0 {
		return nil
	}
	m.byKey[key] = Window{N: n}.Apply(cur)
	return nil
}

// Len 返回某个 key 的条目数。
func (m *Memory) Len(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byKey[key])
}

// Keys 返回当前的 key 列表。
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.byKey))
	for k := range m.byKey {
		out = append(out, k)
	}
	return out
}

var _ History = (*Memory)(nil)
