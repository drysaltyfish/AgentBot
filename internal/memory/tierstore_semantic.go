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
	// 固化传来的条目带着归属人（Working 写入时记下的发言人 QQ 号）；
	// 放进 ctx 让 F-87 的判定与落库都按同一个人比较，而不是退化成"无归属"。
	if item.SubjectID > 0 {
		scoped = scope.WithSubject(scoped, item.SubjectID)
	}
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
		SubjectID: m.SubjectID,
		CreatedAt: time.UnixMilli(m.CreatedAt),
	}
}

// CompositeTierStore 把两层拼成一个 TierStore：过程层（Working/Episodic）用
// tier 表，长期事实层用 F-87 的扁平记忆表。
//
// 三个字段各自持有**窄接口**，所有方法都显式转发。这里曾经是"嵌入 TierStore
// 再覆盖恰好四个语义方法"，并且靠注释保证覆盖集合不多不少——多一个会让某层被
// 悄悄绕过，少一个会让写入进错表，两者都不会编译失败。现在任何一层新增方法，
// 这个类型都会因为缺方法而编译不过。
type CompositeTierStore struct {
	working  WorkingStore
	episodes EpisodeStore
	semantic SemanticStore
}

// NewCompositeTierStore 构造；items 提供过程层（Working + Episodic），
// semantic 提供长期事实层（为 nil 时语义方法会明确报错，不静默丢数据）。
func NewCompositeTierStore(items TierStore, semantic SemanticStore) *CompositeTierStore {
	return &CompositeTierStore{working: items, episodes: items, semantic: semantic}
}

// ---- 过程层：Working ----

// AppendWorking 实现 TierStore。
func (c *CompositeTierStore) AppendWorking(ctx context.Context, scope string, item TierItem) (TierItem, error) {
	return c.working.AppendWorking(ctx, scope, item)
}

// Working 实现 TierStore。
func (c *CompositeTierStore) Working(ctx context.Context, scope string) ([]TierItem, error) {
	return c.working.Working(ctx, scope)
}

// TrimWorking 实现 TierStore。
func (c *CompositeTierStore) TrimWorking(ctx context.Context, scope string, keep int) ([]TierItem, error) {
	return c.working.TrimWorking(ctx, scope, keep)
}

// DeleteWorking 实现 TierStore。
func (c *CompositeTierStore) DeleteWorking(ctx context.Context, scope string, id int64) (bool, error) {
	return c.working.DeleteWorking(ctx, scope, id)
}

// UpdateWorking 实现 TierStore。
func (c *CompositeTierStore) UpdateWorking(ctx context.Context, scope string, id int64, item TierItem) (bool, error) {
	return c.working.UpdateWorking(ctx, scope, id, item)
}

// ---- 过程层：Episodic ----

// AppendEpisode 实现 TierStore。
func (c *CompositeTierStore) AppendEpisode(ctx context.Context, scope string, ep Episode) (Episode, error) {
	return c.episodes.AppendEpisode(ctx, scope, ep)
}

// Episodes 实现 TierStore。
func (c *CompositeTierStore) Episodes(ctx context.Context, scope string, limit int) ([]Episode, error) {
	return c.episodes.Episodes(ctx, scope, limit)
}

// TrimEpisodes 实现 TierStore。
func (c *CompositeTierStore) TrimEpisodes(ctx context.Context, scope string, keep int) (int, error) {
	return c.episodes.TrimEpisodes(ctx, scope, keep)
}

// DeleteEpisode 实现 TierStore。
func (c *CompositeTierStore) DeleteEpisode(ctx context.Context, scope string, id int64) (bool, error) {
	return c.episodes.DeleteEpisode(ctx, scope, id)
}

// DeleteEpisodeItem 实现 TierStore。
func (c *CompositeTierStore) DeleteEpisodeItem(ctx context.Context, scope string, itemID int64) (bool, error) {
	return c.episodes.DeleteEpisodeItem(ctx, scope, itemID)
}

// ---- 长期事实层：全部转发给 semantic，未配置时明确报错 ----

func (c *CompositeTierStore) semanticStore() (SemanticStore, error) {
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
var _ SemanticStore = (*SemanticTierStore)(nil)
var _ MemoryAdminSource = (*SemanticTierStore)(nil)
var _ MemoryAdminSource = (*CompositeTierStore)(nil)

// 三个窄接口各自的实现断言：任何一层漏了方法都会在第一处编译失败，
// 而不是等到组合时才发现。
var (
	_ WorkingStore  = (*MemTierStore)(nil)
	_ EpisodeStore  = (*MemTierStore)(nil)
	_ SemanticStore = (*MemTierStore)(nil)
	_ WorkingStore  = (*SQLiteTierStore)(nil)
	_ EpisodeStore  = (*SQLiteTierStore)(nil)
	_ SemanticStore = (*SQLiteTierStore)(nil)
)
