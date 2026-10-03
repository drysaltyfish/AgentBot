package imagehash

import (
	"container/list"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 缓存与消息段回写相关的常量，见 FEATURES.md F-62。
const (
	// DerivedDescKey 是"已派生描述"写回消息段的字段名。
	//
	// 命中缓存后由调用方把它写进图片段的 Data；再次处理同一事件时可据此
	// 直接复用描述，不再调用视觉模型（幂等）。
	DerivedDescKey = "__derived_desc__"

	// DefaultThreshold 是缓存命中的默认最大汉明距离。
	DefaultThreshold = 8
	// DefaultTTL 是缓存条目的默认存活时间（自写入时刻起算）。
	DefaultTTL = 24 * time.Hour
	// DefaultMaxEntries 是缓存的默认容量上限。
	DefaultMaxEntries = 10000
	// DefaultMaxDescRunes 是单条描述的默认最大 rune 数，超出截断。
	DefaultMaxDescRunes = 512
)

// CacheOptions 配置 Cache。零值字段替换为默认值：
// Threshold=8、TTL=24h、MaxEntries=10000、MaxDescRunes=512、Buckets=自动。
//
// 参数必须可配置而非硬编码（规格 line 1822）。
type CacheOptions struct {
	// Threshold 是命中所需的最大汉明距离，<=0 取 DefaultThreshold(8)，
	// 上限钳到 64。完全相同哈希另有 O(1) 精确快路径。
	Threshold int
	// TTL 是条目存活时间，<=0 取 DefaultTTL。
	TTL time.Duration
	// MaxEntries 是容量上限，<=0 取 DefaultMaxEntries。
	MaxEntries int
	// MaxDescRunes 是单条描述的最大 rune 数，<=0 取 DefaultMaxDescRunes。
	MaxDescRunes int
	// Buckets 是索引块数（每个条目按各前缀块各索引一次）。<=0 或小于
	// threshold+1 时自动抬高到 threshold+1，以维持鸽巢原理的"不漏判"保证。
	Buckets int
	// Now 注入时钟以便测试 TTL，nil 表示 time.Now。
	Now func() time.Time
}

// Cache 是 pHash → 描述文本 的近似缓存。
//
// 命中判定：Lookup 与条目哈希的汉明距离 <= Threshold。结果按哈希前缀块
// 分桶索引，查询只比较同桶候选，不做全量线性扫描（见 effectiveBuckets）。
//
// 双重淘汰：
//   - TTL：自写入时刻起算，过期即不可命中，并在 Store/Lookup 时惰性清除；
//   - LRU：容量溢出时淘汰"最久未被 Lookup/Store 命中"的条目。
//
// 描述写入前按 rune 截断到 MaxDescRunes。所有方法并发安全。
type Cache struct {
	mu sync.Mutex

	threshold    int
	ttl          time.Duration
	maxEntries   int
	maxDescRunes int
	blocks       int
	now          func() time.Time

	byHash  map[Hash]*cacheEntry
	buckets map[uint64]map[*cacheEntry]struct{}
	lru     *list.List // 队首最近使用，队尾最久未用
	expiry  *list.List // 队首最早过期（TTL 均匀，等价写入顺序）
	cmp     atomic.Int64
}

// cacheEntry 是缓存中的一条记录。
type cacheEntry struct {
	hash     Hash
	desc     string
	expireAt time.Time
	lruElem  *list.Element
	expElem  *list.Element
}

// NewCache 构造缓存并填充默认值。
func NewCache(opts CacheOptions) *Cache {
	// 阈值 0 与"未设置"无法区分，统一切到默认 8（规格默认值）。
	if opts.Threshold <= 0 {
		opts.Threshold = DefaultThreshold
	}
	if opts.Threshold > hashBits {
		opts.Threshold = hashBits
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultMaxEntries
	}
	if opts.MaxDescRunes <= 0 {
		opts.MaxDescRunes = DefaultMaxDescRunes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Cache{
		threshold:    opts.Threshold,
		ttl:          opts.TTL,
		maxEntries:   opts.MaxEntries,
		maxDescRunes: opts.MaxDescRunes,
		blocks:       effectiveBuckets(opts.Threshold, opts.Buckets),
		now:          opts.Now,
		byHash:       make(map[Hash]*cacheEntry),
		buckets:      make(map[uint64]map[*cacheEntry]struct{}),
		lru:          list.New(),
		expiry:       list.New(),
	}
}

// effectiveBuckets 计算实际索引块数。
//
// 鸽巢原理：两个哈希汉明距离 d <= threshold 时，把 64 位切成 threshold+1 个
// 互不重叠的位块，至少有一个块完全相同，因此必定落在同一桶中。故块数不得
// 少于 threshold+1（threshold=64 退化，Lookup 单独处理）。
func effectiveBuckets(threshold, requested int) int {
	need := threshold + 1
	if need > hashBits {
		need = hashBits
	}
	if need < 1 {
		need = 1
	}
	if requested < need {
		return need
	}
	if requested > hashBits {
		return hashBits
	}
	return requested
}

// Lookup 查询与 h 近似（距离 <= Threshold）且未过期的条目，返回其描述。
//
// 精确哈希命中走 O(1) 快路径；近似命中只扫描 h 各前缀块对应的候选桶。
// 命中会刷新该条目的 LRU 新鲜度（但不延长 TTL）。
func (c *Cache) Lookup(h Hash) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if e, ok := c.byHash[h]; ok {
		if now.Before(e.expireAt) {
			c.lru.MoveToFront(e.lruElem)
			return e.desc, true
		}
		c.removeLocked(e)
	}

	// threshold=64 时任意哈希都算命中：任取一条未过期条目即可。
	if c.threshold >= hashBits {
		c.purgeExpiredLocked(now)
		if f := c.expiry.Front(); f != nil {
			e := f.Value.(*cacheEntry)
			c.lru.MoveToFront(e.lruElem)
			return e.desc, true
		}
		return "", false
	}

	var (
		best     *cacheEntry
		bestDist = c.threshold + 1
		expired  []*cacheEntry
	)
	for _, key := range bucketKeys(h, c.blocks) {
		for cand := range c.buckets[key] {
			if !now.Before(cand.expireAt) {
				expired = append(expired, cand)
				continue
			}
			c.cmp.Add(1)
			if d := Distance(cand.hash, h); d < bestDist {
				best, bestDist = cand, d
				if d == 0 {
					break
				}
			}
		}
	}
	for _, e := range expired {
		c.removeLocked(e)
	}
	if best == nil {
		return "", false
	}
	c.lru.MoveToFront(best.lruElem)
	return best.desc, true
}

// LookupSegments 在 Lookup 命中时把描述写回给定消息段的 __derived_desc__ 字段，
// 返回描述与是否命中。这样调用方一次调用即可实现规格要求的"命中后写回"。
//
// segments 以段序号为键、段 Data 为值；nil 或无命中的段会被跳过。
func (c *Cache) LookupSegments(h Hash, segments map[int]map[string]string) (string, bool) {
	desc, ok := c.Lookup(h)
	if !ok {
		return "", false
	}
	SetDerivedDescAll(segments, desc)
	return desc, true
}

// Store 写入或更新 h → desc。desc 超出 MaxDescRunes 时按 rune 截断。
//
// 重复写入同一哈希会刷新 TTL 与 LRU 位置；容量溢出时淘汰最久未用条目。
func (c *Cache) Store(h Hash, desc string) {
	desc = truncateRunes(desc, c.maxDescRunes)
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.purgeExpiredLocked(now)

	if e, ok := c.byHash[h]; ok {
		e.desc = desc
		e.expireAt = now.Add(c.ttl)
		if e.expElem != nil {
			c.expiry.MoveToBack(e.expElem)
		}
		c.lru.MoveToFront(e.lruElem)
		return
	}

	e := &cacheEntry{hash: h, desc: desc, expireAt: now.Add(c.ttl)}
	e.lruElem = c.lru.PushFront(e)
	e.expElem = c.expiry.PushBack(e)
	c.byHash[h] = e
	for _, key := range bucketKeys(h, c.blocks) {
		m := c.buckets[key]
		if m == nil {
			m = make(map[*cacheEntry]struct{})
			c.buckets[key] = m
		}
		m[e] = struct{}{}
	}

	for len(c.byHash) > c.maxEntries {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.removeLocked(back.Value.(*cacheEntry))
	}
}

// Len 返回当前未（主动）淘汰的条目数，恒不超过 MaxEntries。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byHash)
}

// Comparisons 返回累计执行的哈希距离计算次数，供测试断言分桶有效（比较次数
// 远小于条目数）。精确快路径不计入。
func (c *Cache) Comparisons() int64 {
	return c.cmp.Load()
}

// ResetComparisons 清零距离比较计数。
func (c *Cache) ResetComparisons() {
	c.cmp.Store(0)
}

// purgeExpiredLocked 从 expiry 队首清除已过期条目（TTL 均匀且时钟单调时，
// 队首即最早过期，故摊还 O(1)）。调用方须持锁。
func (c *Cache) purgeExpiredLocked(now time.Time) {
	for {
		f := c.expiry.Front()
		if f == nil {
			return
		}
		e := f.Value.(*cacheEntry)
		if now.Before(e.expireAt) {
			return
		}
		c.removeLocked(e)
	}
}

// removeLocked 从精确表、桶索引、LRU 与 expiry 链中摘除 e。
// 以 lruElem==nil 作为已摘除标记，重复调用安全。
func (c *Cache) removeLocked(e *cacheEntry) {
	if e.lruElem == nil {
		return
	}
	if cur, ok := c.byHash[e.hash]; ok && cur == e {
		delete(c.byHash, e.hash)
	}
	for _, key := range bucketKeys(e.hash, c.blocks) {
		if m := c.buckets[key]; m != nil {
			delete(m, e)
			if len(m) == 0 {
				delete(c.buckets, key)
			}
		}
	}
	c.lru.Remove(e.lruElem)
	if e.expElem != nil {
		c.expiry.Remove(e.expElem)
	}
	e.lruElem = nil
	e.expElem = nil
}

// truncateRunes 按 rune 截断到 max 个字符，避免切断多字节字符。max<=0 不截断。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// SetDerivedDesc 把一个消息段的 Data 写入 __derived_desc__ 字段（原地修改）。
//
// 只依赖 map，不导入 internal/event，避免包耦合。空/纯空白描述不写。
// 返回是否写入。
func SetDerivedDesc(seg map[string]string, desc string) bool {
	if seg == nil {
		return false
	}
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return false
	}
	seg[DerivedDescKey] = desc
	return true
}

// SetDerivedDescAll 把描述写入以段序号索引的多个消息段 Data，返回写入段数。
func SetDerivedDescAll(segments map[int]map[string]string, desc string) int {
	n := 0
	for _, seg := range segments {
		if SetDerivedDesc(seg, desc) {
			n++
		}
	}
	return n
}
