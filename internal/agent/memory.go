package agent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

var (
	// ErrEmptyMemory 表示要保存的记忆为空。
	ErrEmptyMemory = errors.New("memory text is empty")
	// ErrMultilineMemory 表示记忆含换行（F-40 要求单行）。
	ErrMultilineMemory = errors.New("memory text must be a single line")
	// ErrMemoryTooLong 表示记忆超长。
	ErrMemoryTooLong = errors.New("memory text is too long")
	// ErrMemoryUnavailable 表示记忆存储未配置。
	ErrMemoryUnavailable = errors.New("memory store is not configured")
)

// MemoryLimit 是单条记忆的长度上限（字符）。
//
// 取值对齐 F-47："单条记忆长度上限（默认 500 字符）"。
const MemoryLimit = 500

// Memory 是虚拟动作 save_memory / memory_recall 依赖的最小长期记忆能力。
//
// F-47 的完整接口（Scope / MemoryItem / Forget / List）与 F-48 的实现在 M3；
// 这里只定义最小能力，让 F-40 的闭环先跑起来。
//
// **作用域经 ctx 传递**（见 WithMemoryScope），而不是加进方法签名：
// 工具的执行签名是 Execute(ctx, args)，把它改了会波及所有工具；
// 而作用域本来就是"这次调用属于谁"的上下文信息。
type Memory interface {
	Save(ctx context.Context, text string) error
	Recall(ctx context.Context) ([]string, error)
}

// WithMemoryScope 把记忆作用域放进 ctx。
//
// 作用域隔离是 F-47 的硬要求："群 A 的记忆不得出现在群 B 的回忆中"。
// 不隔离的话，私聊里存下的内容会被注入群聊的提示词——这是隐私缺陷。
//
// 实现下沉在 tool 包：内置工具（如 recall_history）也需要按会话取值，
// 而 builtin 不该反向依赖 agent。
func WithMemoryScope(ctx context.Context, scope string) context.Context {
	return tool.WithScope(ctx, scope)
}

// MemoryScopeFrom 取出 ctx 里的作用域；没有时返回空串（默认桶）。
func MemoryScopeFrom(ctx context.Context) string {
	return tool.ScopeFrom(ctx)
}

// validateMemoryText 做写入前的统一校验。
//
// 两种实现（进程内 / 落盘）共用同一套校验，避免换个实现就少一条约束。
// 刻意返回错误而不是截断：截断会让模型以为整条存下来了。
func validateMemoryText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", ErrEmptyMemory
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", ErrMultilineMemory
	}
	if len([]rune(trimmed)) > MemoryLimit {
		return "", ErrMemoryTooLong
	}
	return trimmed, nil
}

// MemoryStore 是进程内记忆实现，**按作用域隔离**。
//
// 顺序即写入顺序：只要写入序列相同，两次召回的结果就逐字节相同——这是记忆段
// 不白白多失效一次缓存的前提（ADR-0002）。
type MemoryStore struct {
	mu      sync.Mutex
	byScope map[string][]string
	max     int
}

// NewMemoryStore 构造进程内记忆；max 是**每个作用域**的条数上限，<=0 时用 64。
func NewMemoryStore(max int) *MemoryStore {
	if max <= 0 {
		max = 64
	}
	return &MemoryStore{byScope: map[string][]string{}, max: max}
}

// Save 追加一条记忆到 ctx 指定的作用域。
//
// 校验在写入前完成：空、含换行、超长都直接拒绝——拒绝理由会被回灌给模型，
// 让它自己修正，而不是静默截断（截断会让模型以为整条存下来了）。
func (m *MemoryStore) Save(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	trimmed, err := validateMemoryText(text)
	if err != nil {
		return err
	}

	scope := MemoryScopeFrom(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.byScope[scope]
	// 去重：重复写入不追加，保证同样的写入序列得到同样的结果。
	for _, existing := range items {
		if existing == trimmed {
			return nil
		}
	}
	if len(items) >= m.max {
		// 超出上限时丢弃最旧的一条，保持有界。
		items = append(items[:0], items[1:]...)
	}
	m.byScope[scope] = append(items, trimmed)
	return nil
}

// Recall 按写入顺序返回 ctx 指定作用域的记忆副本。
func (m *MemoryStore) Recall(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope := MemoryScopeFrom(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.byScope[scope]
	out := make([]string, len(src))
	copy(out, src)
	return out, nil
}

// Len 返回全部分作用域的记忆条数。
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, items := range m.byScope {
		total += len(items)
	}
	return total
}

// LenScope 返回某个作用域的条数。
func (m *MemoryStore) LenScope(scope string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byScope[scope])
}

// Reset 清空某个作用域（F-47 的 Forget 在 M3 接入）。
func (m *MemoryStore) Reset(ctx context.Context) {
	scope := MemoryScopeFrom(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byScope, scope)
}

var _ Memory = (*MemoryStore)(nil)

// HistoryMemory 把记忆落在 history.History 上（F-47 的"可选 JSONL 文件落盘"）。
//
// 复用 F-38 的历史存储而不是另写一套文件格式：JSONL 编码、权限、裁剪、并发都已经
// 在那里经过测试。落盘条目用 KindMarker：它不是对话轮次，即便同一份文件被当作
// 聊天历史读取，conversation.ToMessages 也会跳过它，不会误入提示词。
type HistoryMemory struct {
	hist history.History
}

// NewHistoryMemory 构造落盘记忆。
func NewHistoryMemory(h history.History) *HistoryMemory {
	return &HistoryMemory{hist: h}
}

// Save 实现 Memory。
func (m *HistoryMemory) Save(ctx context.Context, text string) error {
	if m == nil || m.hist == nil {
		return ErrMemoryUnavailable
	}
	trimmed, err := validateMemoryText(text)
	if err != nil {
		return err
	}
	scope := MemoryScopeFrom(ctx)

	// 去重：同样的写入序列必须得到同样的召回结果，否则记忆段会平白多失效一次缓存。
	existing, err := m.Recall(ctx)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e == trimmed {
			return nil
		}
	}
	return m.hist.Append(ctx, scope, history.Item{Kind: history.KindMarker, Content: trimmed})
}

// Recall 实现 Memory。
func (m *HistoryMemory) Recall(ctx context.Context) ([]string, error) {
	if m == nil || m.hist == nil {
		return nil, ErrMemoryUnavailable
	}
	items, err := m.hist.Messages(ctx, MemoryScopeFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Content)
	}
	return out, nil
}

var _ Memory = (*HistoryMemory)(nil)
