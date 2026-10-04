package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/drysaltyfish/agentbot/internal/scope"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// MemoryAdminSource 是"语义层能直接给出 store.Memory 视图"的可选能力。
//
// 单独定义而不是塞进 TierStore：只有基于 F-87 扁平表的语义层能提供字段完整的
// 记忆视图（作用域、时间戳、来源引用），内存实现给不出来。可选能力让
// TieredMemory 在没有它时优雅退化，而不是伪造一堆零值字段。
type MemoryAdminSource interface {
	ListMemories(ctx context.Context, scopeKey string, limit int) ([]store.Memory, error)
	ForgetMemory(ctx context.Context, scopeKey string, id int64) (bool, error)
	ForgetScope(ctx context.Context, scopeKey string) (int, error)
}

// SemanticTierStore 用 F-87 的扁平记忆表充当分层记忆（F-49）的 Semantic 层。
//
// 为什么这样接：F-87 已经有一整套写入判定（指纹 -> 相似度分带 -> 判官）以及
// 遗忘/检视实现，并且已被 F-88 的 forget_memory / list_memories 使用。把
// Semantic 单独实现一遍，等于让同一件事存在两套判定，而两套判定迟早会漂移——
// 用户会看到"工具说记忆被合并了，召回里却有两条"。
//
// Working / Episodic 仍由 tier_items 等表承担（见 SQLiteTierStore）。
type SemanticTierStore struct {
	mem *Store
}

// NewSemanticTierStore 构造；mem 为 nil 时所有方法明确报错，不静默丢数据。
func NewSemanticTierStore(mem *Store) *SemanticTierStore { return &SemanticTierStore{mem: mem} }

func (s *SemanticTierStore) ready() error {
	if s == nil || s.mem == nil || s.mem.st == nil {
		return ErrUnavailable
	}
	return nil
}

// UpsertSemantic 走 F-87 的写入判定；返回是否**新增**（合并或忽略时为 false）。
func (s *SemanticTierStore) UpsertSemantic(ctx context.Context, scopeKey string, item TierItem) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	scoped := scope.WithScope(ctx, scopeKey)
	res, err := s.mem.SaveItem(scoped, item.Text)
	if err != nil {
		return false, err
	}
	return res.Decision == store.MemoryAdded, nil
}

// Semantics 按写入顺序返回长期事实。
func (s *SemanticTierStore) Semantics(ctx context.Context, scopeKey string) ([]TierItem, error) {
	items, err := s.ListMemories(ctx, scopeKey, 0)
	if err != nil {
		return nil, err
	}
	out := make([]TierItem, 0, len(items))
	for _, m := range items {
		out = append(out, tierItemFromMemory(m))
	}
	return out, nil
}

// TrimSemantics 把作用域淘汰到 keep 条。
func (s *SemanticTierStore) TrimSemantics(ctx context.Context, scopeKey string, keep int) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	return s.mem.st.TrimMemories(ctx, scopeKey, keep)
}

// DeleteSemantic 删除一条长期事实。
func (s *SemanticTierStore) DeleteSemantic(ctx context.Context, scopeKey string, id int64) (bool, error) {
	return s.ForgetMemory(ctx, scopeKey, id)
}

// ListMemories 实现 MemoryAdminSource。
func (s *SemanticTierStore) ListMemories(ctx context.Context, scopeKey string, limit int) ([]store.Memory, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.mem.st.ListMemories(ctx, scopeKey, limit)
}

// ForgetMemory 实现 MemoryAdminSource。
func (s *SemanticTierStore) ForgetMemory(ctx context.Context, scopeKey string, id int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	return s.mem.st.ForgetMemory(ctx, scopeKey, id)
}

// ForgetScope 实现 MemoryAdminSource。
func (s *SemanticTierStore) ForgetScope(ctx context.Context, scopeKey string) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	return s.mem.st.ForgetScope(ctx, scopeKey)
}

func tierItemFromMemory(m store.Memory) TierItem {
	return TierItem{
		ID:        m.ID,
		Text:      m.Text,
		Title:     m.Title,
		Tier:      TierSemantic,
		Score:     m.Score,
		CreatedAt: time.UnixMilli(m.CreatedAt),
	}
}

// SemanticLayer 是长期事实层需要实现的方法集，正好是 TierStore 的四个语义方法。
//
// 单独抽出来是为了让 CompositeTierStore 接受一个"只做语义"的实现：
// SemanticTierStore 故意不实现 Working/Episodic，它没有那张表。
type SemanticLayer interface {
	UpsertSemantic(ctx context.Context, scopeKey string, item TierItem) (bool, error)
	Semantics(ctx context.Context, scopeKey string) ([]TierItem, error)
	TrimSemantics(ctx context.Context, scopeKey string, keep int) (int, error)
	DeleteSemantic(ctx context.Context, scopeKey string, id int64) (bool, error)
}

// CompositeTierStore 把两层拼成一个 TierStore：过程层（Working/Episodic）用
// tier 表，长期事实层用 F-87 的扁平记忆表。
//
// 嵌入 TierStore 提供全部过程层方法；下面正好覆盖四个语义方法。覆盖集合必须
// 恰好是这四个——多一个会让某层被悄悄绕过，少一个会让写入进错表。
type CompositeTierStore struct {
	TierStore
	semantic SemanticLayer
}

// NewCompositeTierStore 构造；semantic 为 nil 时语义方法会明确报错。
func NewCompositeTierStore(items TierStore, semantic SemanticLayer) *CompositeTierStore {
	return &CompositeTierStore{TierStore: items, semantic: semantic}
}

func (c *CompositeTierStore) semanticStore() (SemanticLayer, error) {
	if c == nil || c.semantic == nil {
		return nil, fmt.Errorf("tiered memory: semantic layer is not configured")
	}
	return c.semantic, nil
}

// UpsertSemantic 实现 TierStore。
func (c *CompositeTierStore) UpsertSemantic(ctx context.Context, scopeKey string, item TierItem) (bool, error) {
	s, err := c.semanticStore()
	if err != nil {
		return false, err
	}
	return s.UpsertSemantic(ctx, scopeKey, item)
}

// Semantics 实现 TierStore。
func (c *CompositeTierStore) Semantics(ctx context.Context, scopeKey string) ([]TierItem, error) {
	s, err := c.semanticStore()
	if err != nil {
		return nil, err
	}
	return s.Semantics(ctx, scopeKey)
}

// TrimSemantics 实现 TierStore。
func (c *CompositeTierStore) TrimSemantics(ctx context.Context, scopeKey string, keep int) (int, error) {
	s, err := c.semanticStore()
	if err != nil {
		return 0, err
	}
	return s.TrimSemantics(ctx, scopeKey, keep)
}

// DeleteSemantic 实现 TierStore。
func (c *CompositeTierStore) DeleteSemantic(ctx context.Context, scopeKey string, id int64) (bool, error) {
	s, err := c.semanticStore()
	if err != nil {
		return false, err
	}
	return s.DeleteSemantic(ctx, scopeKey, id)
}

// ListMemories 透出 F-88 的检视能力；语义层不支持时返回 false。
func (c *CompositeTierStore) ListMemories(ctx context.Context, scopeKey string, limit int) ([]store.Memory, error) {
	src, ok := c.adminSource()
	if !ok {
		return nil, ErrUnavailable
	}
	return src.ListMemories(ctx, scopeKey, limit)
}

// ForgetMemory 透出 F-88 的遗忘能力；语义层不支持时返回 false。
func (c *CompositeTierStore) ForgetMemory(ctx context.Context, scopeKey string, id int64) (bool, error) {
	src, ok := c.adminSource()
	if !ok {
		return false, ErrUnavailable
	}
	return src.ForgetMemory(ctx, scopeKey, id)
}

// ForgetScope 透出 F-88 的清空能力；语义层不支持时返回 false。
func (c *CompositeTierStore) ForgetScope(ctx context.Context, scopeKey string) (int, error) {
	src, ok := c.adminSource()
	if !ok {
		return 0, ErrUnavailable
	}
	return src.ForgetScope(ctx, scopeKey)
}

func (c *CompositeTierStore) adminSource() (MemoryAdminSource, bool) {
	if c == nil || c.semantic == nil {
		return nil, false
	}
	src, ok := c.semantic.(MemoryAdminSource)
	return src, ok
}

var _ TierStore = (*CompositeTierStore)(nil)
var _ SemanticLayer = (*SemanticTierStore)(nil)
var _ MemoryAdminSource = (*SemanticTierStore)(nil)
var _ MemoryAdminSource = (*CompositeTierStore)(nil)
