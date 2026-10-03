package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var (
	// ErrEmptyMemory 表示要保存的记忆为空。
	ErrEmptyMemory = errors.New("memory text is empty")
	// ErrMultilineMemory 表示记忆含换行（F-40 要求单行）。
	ErrMultilineMemory = errors.New("memory text must be a single line")
	// ErrMemoryTooLong 表示记忆超长。
	ErrMemoryTooLong = errors.New("memory text is too long")
)

// MemoryLimit 是单条记忆的长度上限（与 ADR-0002 的 2 KiB 一致）。
const MemoryLimit = 2048

// Memory 是虚拟动作 save_memory / memory_recall 依赖的最小长期记忆能力。
//
// F-48 的完整实现（持久化、向量召回、TTL）在 M3；这里只定义接口 + 进程内实现，
// 让 F-40 的闭环可以先跑起来。接口保持最小，避免把 M3 的设计提前钉死。
type Memory interface {
	Save(ctx context.Context, text string) error
	Recall(ctx context.Context) ([]string, error)
}

// MemoryStore 是进程内记忆实现。
//
// 顺序即写入顺序：只要写入序列相同，两次召回的结果就逐字节相同——这是记忆段
// 不白白多失效一次的前提（ADR-0002）。
type MemoryStore struct {
	mu    sync.Mutex
	items []string
	max   int
}

// NewMemoryStore 构造进程内记忆；max <= 0 时使用 64 条。
func NewMemoryStore(max int) *MemoryStore {
	if max <= 0 {
		max = 64
	}
	return &MemoryStore{max: max}
}

// Save 追加一条记忆。
//
// 校验在写入前完成：空、含换行、超长都直接拒绝——拒绝理由会被回灌给模型，
// 让它自己修正，而不是静默丢弃。
func (m *MemoryStore) Save(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ErrEmptyMemory
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return ErrMultilineMemory
	}
	if len([]rune(trimmed)) > MemoryLimit {
		return ErrMemoryTooLong
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 去重：重复写入不追加，保证同样的写入序列得到同样的结果。
	for _, existing := range m.items {
		if existing == trimmed {
			return nil
		}
	}
	if len(m.items) >= m.max {
		// 超出上限时丢弃最旧的一条，保持有界。
		m.items = append(m.items[:0], m.items[1:]...)
	}
	m.items = append(m.items, trimmed)
	return nil
}

// Recall 按写入顺序返回记忆副本。
func (m *MemoryStore) Recall(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.items))
	copy(out, m.items)
	return out, nil
}

// Len 返回记忆条数。
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

var _ Memory = (*MemoryStore)(nil)
