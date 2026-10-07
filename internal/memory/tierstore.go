package memory

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/store"
)

// TierStore 是分层记忆（F-49）的持久化接口。
//
// 这里只定义 F-49 真正用到的方法：Lead 接线 SQLite 时按报告中的表结构实现即可
// （tier_working / tier_episodes / tier_episode_items / tier_semantic），
// 内存实现 MemTierStore 可直接用于测试与单进程部署。
//
// 所有方法都必须按 scope 严格隔离：scope 必须参与每一条读写路径（F-47）。
type TierStore interface {
	// AppendWorking 追加一条 Working 记录并返回带 ID 的副本。
	AppendWorking(ctx context.Context, scope string, item TierItem) (TierItem, error)
	// Working 返回该作用域的 Working 记录，按创建时间升序。
	Working(ctx context.Context, scope string) ([]TierItem, error)
	// TrimWorking 把 Working 裁剪到最新 keep 条，返回被移除的条目（供固化）。
	TrimWorking(ctx context.Context, scope string, keep int) ([]TierItem, error)
	// DeleteWorking 按 id 删除一条 Working 记录。
	DeleteWorking(ctx context.Context, scope string, id int64) (bool, error)
	// UpdateWorking 就地改写一条 Working 记录的正文与归属人（保留 id 与顺序）。
	// 返回 false 表示该条目已不存在（可能刚被删除），调用方应退回追加而不是丢弃。
	UpdateWorking(ctx context.Context, scope string, id int64, item TierItem) (bool, error)

	// AppendEpisode 追加一个会话片段并返回带 ID 的副本。
	AppendEpisode(ctx context.Context, scope string, ep Episode) (Episode, error)
	// Episodes 返回片段，按开始时间升序；limit<=0 表示全部。
	Episodes(ctx context.Context, scope string, limit int) ([]Episode, error)
	// TrimEpisodes 保留最新 keep 个片段，返回被淘汰的数量。
	TrimEpisodes(ctx context.Context, scope string, keep int) (int, error)
	// DeleteEpisode 按 id 删除一个片段。
	DeleteEpisode(ctx context.Context, scope string, id int64) (bool, error)
	// DeleteEpisodeItem 按条目 id 从任意片段中删除该条目；片段清空时一并删除。
	DeleteEpisodeItem(ctx context.Context, scope string, itemID int64) (bool, error)

	// UpsertSemantic 按文本就地更新或新增一条长期事实；返回是否为新增。
	UpsertSemantic(ctx context.Context, scope string, item TierItem) (bool, error)
	// Semantics 返回该作用域的全部长期事实，按写入顺序。
	Semantics(ctx context.Context, scope string) ([]TierItem, error)
	// TrimSemantics 淘汰多余的长期事实（低分优先、同分更旧优先），返回淘汰数。
	TrimSemantics(ctx context.Context, scope string, keep int) (int, error)
	// DeleteSemantic 按 id 删除一条长期事实。
	DeleteSemantic(ctx context.Context, scope string, id int64) (bool, error)
}

// MemTierStore 是 TierStore 的进程内实现，零值可直接使用。
//
// 它按作用域持有三层的切片；所有导出方法都在同一把锁下操作，
// 并统一返回副本，保证调用方修改不影响内部（F-47 的 Recall 副本要求）。
type MemTierStore struct {
	mu       sync.Mutex
	nextID   int64
	working  map[string][]TierItem
	episodes map[string][]Episode
	semantic map[string][]TierItem
}

// ensureLocked 惰性初始化内部 map，使零值可用。调用方必须持锁。
func (m *MemTierStore) ensureLocked() {
	if m.working == nil {
		m.working = make(map[string][]TierItem)
	}
	if m.episodes == nil {
		m.episodes = make(map[string][]Episode)
	}
	if m.semantic == nil {
		m.semantic = make(map[string][]TierItem)
	}
}

// newIDLocked 分配一个单调递增的 ID。调用方必须持锁。
func (m *MemTierStore) newIDLocked() int64 {
	m.nextID++
	return m.nextID
}

// cloneTierItem 返回条目的深拷贝（Refs 也复制）。
func cloneTierItem(it TierItem) TierItem {
	it.Refs = slices.Clone(it.Refs)
	return it
}

// cloneTierItems 返回条目切片的深拷贝。
func cloneTierItems(in []TierItem) []TierItem {
	if in == nil {
		return nil
	}
	out := make([]TierItem, len(in))
	for i := range in {
		out[i] = cloneTierItem(in[i])
	}
	return out
}

// cloneEpisode 返回片段的深拷贝。
func cloneEpisode(ep Episode) Episode {
	ep.Items = cloneTierItems(ep.Items)
	return ep
}

// AppendWorking 实现 TierStore。
func (m *MemTierStore) AppendWorking(_ context.Context, scope string, item TierItem) (TierItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	item.Tier = TierWorking
	item.ID = m.newIDLocked()
	m.working[scope] = append(m.working[scope], item)
	return cloneTierItem(item), nil
}

// Working 实现 TierStore。
func (m *MemTierStore) Working(_ context.Context, scope string) ([]TierItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	return cloneTierItems(m.working[scope]), nil
}

// TrimWorking 实现 TierStore：保留最新 keep 条，返回被裁掉的旧条目。
func (m *MemTierStore) TrimWorking(_ context.Context, scope string, keep int) ([]TierItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.working[scope]
	if keep < 0 {
		keep = 0
	}
	if keep >= len(list) {
		return nil, nil
	}
	cut := len(list) - keep
	removed := cloneTierItems(list[:cut])
	m.working[scope] = slices.Clone(list[cut:])
	return removed, nil
}

// DeleteWorking 实现 TierStore。
func (m *MemTierStore) DeleteWorking(_ context.Context, scope string, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.working[scope]
	for i := range list {
		if list[i].ID == id {
			m.working[scope] = slices.Delete(slices.Clone(list), i, i+1)
			return true, nil
		}
	}
	return false, nil
}

// UpdateWorking 实现 TierStore：就地改写正文与归属人，保留 id 与切片位置。
func (m *MemTierStore) UpdateWorking(_ context.Context, scope string, id int64, item TierItem) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.working[scope]
	for i := range list {
		if list[i].ID != id {
			continue
		}
		list[i].Text = item.Text
		list[i].SubjectID = item.SubjectID
		if !item.CreatedAt.IsZero() {
			list[i].CreatedAt = item.CreatedAt
		}
		m.working[scope] = list
		return true, nil
	}
	return false, nil
}

// AppendEpisode 实现 TierStore。
func (m *MemTierStore) AppendEpisode(_ context.Context, scope string, ep Episode) (Episode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	ep.ID = m.newIDLocked()
	ep.Tier = TierEpisodic
	for i := range ep.Items {
		ep.Items[i].Tier = TierEpisodic
	}
	m.episodes[scope] = append(m.episodes[scope], ep)
	return cloneEpisode(ep), nil
}

// Episodes 实现 TierStore。
func (m *MemTierStore) Episodes(_ context.Context, scope string, limit int) ([]Episode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.episodes[scope]
	if limit > 0 && limit < len(list) {
		list = list[len(list)-limit:]
	}
	out := make([]Episode, len(list))
	for i := range list {
		out[i] = cloneEpisode(list[i])
	}
	return out, nil
}

// TrimEpisodes 实现 TierStore：片段按追加顺序（最旧在前）淘汰队首。
func (m *MemTierStore) TrimEpisodes(_ context.Context, scope string, keep int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.episodes[scope]
	if keep < 0 {
		keep = 0
	}
	if keep >= len(list) {
		return 0, nil
	}
	dropped := len(list) - keep
	m.episodes[scope] = slices.Clone(list[dropped:])
	return dropped, nil
}

// DeleteEpisode 实现 TierStore。
func (m *MemTierStore) DeleteEpisode(_ context.Context, scope string, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.episodes[scope]
	for i := range list {
		if list[i].ID == id {
			m.episodes[scope] = slices.Delete(slices.Clone(list), i, i+1)
			return true, nil
		}
	}
	return false, nil
}

// DeleteEpisodeItem 实现 TierStore。
func (m *MemTierStore) DeleteEpisodeItem(_ context.Context, scope string, itemID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.episodes[scope]
	found := false
	out := make([]Episode, 0, len(list))
	for i := range list {
		ep := list[i]
		items := make([]TierItem, 0, len(ep.Items))
		for j := range ep.Items {
			if ep.Items[j].ID == itemID {
				found = true
				continue
			}
			items = append(items, ep.Items[j])
		}
		if !found || len(items) == len(ep.Items) {
			out = append(out, ep)
			continue
		}
		if len(items) == 0 {
			continue
		}
		ep.Items = items
		out = append(out, ep)
	}
	m.episodes[scope] = out
	return found, nil
}

// UpsertSemantic 实现 TierStore：按 (scope, text) 指纹就地更新，不改变顺序。
func (m *MemTierStore) UpsertSemantic(_ context.Context, scope string, item TierItem) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	key := store.Fingerprint("semantic", item.Text)
	for i := range m.semantic[scope] {
		existing := m.semantic[scope][i]
		if store.Fingerprint("semantic", existing.Text) != key {
			continue
		}
		existing.Text = item.Text
		existing.Title = item.Title
		existing.Refs = slices.Clone(item.Refs)
		existing.Score = item.Score
		existing.Tier = TierSemantic
		m.semantic[scope][i] = existing
		return false, nil
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	item.Tier = TierSemantic
	item.ID = m.newIDLocked()
	item.Refs = slices.Clone(item.Refs)
	m.semantic[scope] = append(m.semantic[scope], item)
	return true, nil
}

// Semantics 实现 TierStore。
func (m *MemTierStore) Semantics(_ context.Context, scope string) ([]TierItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	return cloneTierItems(m.semantic[scope]), nil
}

// TrimSemantics 实现 TierStore：低分优先淘汰，同分淘汰更旧的，保持幸存者原顺序。
func (m *MemTierStore) TrimSemantics(_ context.Context, scope string, keep int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.semantic[scope]
	if keep < 0 {
		keep = 0
	}
	if keep >= len(list) {
		return 0, nil
	}
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := list[order[a]], list[order[b]]
		if ia.Score != ib.Score {
			return ia.Score < ib.Score
		}
		if !ia.CreatedAt.Equal(ib.CreatedAt) {
			return ia.CreatedAt.Before(ib.CreatedAt)
		}
		return ia.ID < ib.ID
	})
	drop := make(map[int]bool, len(list)-keep)
	for _, idx := range order[:len(list)-keep] {
		drop[idx] = true
	}
	out := make([]TierItem, 0, keep)
	for i := range list {
		if !drop[i] {
			out = append(out, list[i])
		}
	}
	m.semantic[scope] = out
	return len(drop), nil
}

// DeleteSemantic 实现 TierStore。
func (m *MemTierStore) DeleteSemantic(_ context.Context, scope string, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLocked()
	list := m.semantic[scope]
	for i := range list {
		if list[i].ID == id {
			m.semantic[scope] = slices.Delete(slices.Clone(list), i, i+1)
			return true, nil
		}
	}
	return false, nil
}
