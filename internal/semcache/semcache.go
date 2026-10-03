// Package semcache 实现 F-63 的语义缓存。
//
// 它把"相似问题 -> 回答"缓存起来，命中即跳过 LLM。为了保持叶子包属性，
// 它不依赖 internal/llm 或 internal/memory：向量化与相似度都由调用方注入，
// 缺省时使用 internal/vector 的二值编码 + 汉明相似度。
//
// 索引策略：
//   - 用"会话/人格指纹"分桶，天然满足 F-63 的按会话/人格隔离；
//   - 使用默认汉明相似度时，再按二值向量的位带做多索引哈希。若阈值对应
//     的最大汉明距离 d 小于位带数 B，则两个相似向量至少在一条位带上完全相同
//     （鸽巢原理），因此只扫描候选位带即可避免全量扫描；
//   - 注入了自定义 Similarity 时，位带哈希不再有正确性保证，此时退回到
//     同一指纹内的线性扫描。规模由 MaxEntries 硬上限兜底（默认 4096），
//     线性扫描的绝对量有界。
package semcache

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/vector"
)

// F-63 的默认参数。
const (
	// DefaultThreshold 是判定命中的最低相似度。
	DefaultThreshold = 0.95
	// DefaultTTL 是缓存条目的生存时间。
	DefaultTTL = time.Hour
	// DefaultMaxEntries 是缓存容量上限。
	DefaultMaxEntries = 4096
	// DefaultBands 是位带索引的默认位带数（会被向量字节数截断）。
	DefaultBands = 8
	// DefaultMaxAnswerLen 是单条答案的字符数上限，超长拒绝缓存。
	DefaultMaxAnswerLen = 4000
)

// Options 是构造语义缓存的参数；零值经归一化后即规格默认值。
type Options struct {
	// Vectorize 把问题转成二值向量（通常用 vector.Encode）。
	// 为 nil 时退化为"问题文本完全相等"的精确匹配。
	Vectorize func(question string) vector.Binary
	// Similarity 是二值向量的相似度；为 nil 时使用 HammingSimilarity。
	// 注入自定义函数会关闭位带近似索引（改为同指纹内线性扫描，见包注释）。
	Similarity func(a, b vector.Binary) float64
	// Threshold <=0 时用 DefaultThreshold。
	Threshold float64
	// TTL <=0 时用 DefaultTTL。
	TTL time.Duration
	// MaxEntries <=0 时用 DefaultMaxEntries。
	MaxEntries int
	// Bands <=0 时用 DefaultBands。
	Bands int
	// MaxAnswerLen <=0 时用 DefaultMaxAnswerLen。
	MaxAnswerLen int
	// SkipList 是易变话题关键词（子串匹配）。为 nil 时使用内置列表；
	// 显式传空切片表示不启用关键词跳过。
	SkipList []string
	// SkipPatterns 是附加的正则跳过规则，语法错误时 New 返回错误。
	SkipPatterns []string
	// Clock 为 nil 时使用 time.Now；测试可注入确定性时钟。
	Clock func() time.Time
	// TokenCount 估算一条答案的 token 数；为 nil 时按字符数计。
	TokenCount func(answer string) int
	// Filter 是出口过滤链（F-55）：返回 (改写后的答案, 是否允许缓存)。
	// 为 nil 时原样缓存。
	Filter func(answer string) (string, bool)
	// Rewrite 可选：命中后按当前上下文改写答案；为 nil 时不改写。
	Rewrite func(answer string) string
	// Warn 接收降级/拒绝告警。
	Warn func(string)
}

// Stats 是语义缓存的观测计数。
type Stats struct {
	Hits        int
	Misses      int
	Evictions   int
	Expirations int
	Entries     int
	// TokensSaved 是命中时按 TokenCount 累计省下的 token 数。
	TokensSaved int
	// Skipped 是因跳过列表/正则而未缓存或未查询的次数。
	Skipped int
	// Rejected 是被出口过滤链或长度上限拒绝写入的次数。
	Rejected int
}

// HitRate 返回命中率；无查询时为 0。
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// options 是归一化后的配置。
type options struct {
	vectorize    func(string) vector.Binary
	similarity   func(a, b vector.Binary) float64
	threshold    float64
	ttl          time.Duration
	maxEntries   int
	bands        int
	maxAnswerLen int
	clock        func() time.Time
	tokenCount   func(string) int
	filter       func(string) (string, bool)
	rewrite      func(string) string
	warn         func(string)
}

// entry 是缓存中的一条记录。
type entry struct {
	id          int64
	key         vector.Binary
	fingerprint string
	question    string
	answer      string
	createdAt   time.Time
	usedAt      time.Time
	hits        int
	tokens      int
}

// Cache 是并发安全的语义缓存。
type Cache struct {
	opts    options
	skip    *regexp.Regexp
	useBand bool

	mu            sync.Mutex
	entries       map[int64]*entry
	byFingerprint map[string]map[int64]struct{}
	bands         map[string]map[int64]struct{}
	disabled      map[string]bool
	nextID        int64
	stats         Stats
}

// New 构造语义缓存；仅当跳过正则无法编译时返回错误。
func New(opts Options) (*Cache, error) {
	skip, err := compileSkip(opts.SkipList, opts.SkipPatterns)
	if err != nil {
		return nil, err
	}
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	if threshold > 1 {
		threshold = 1
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	bands := opts.Bands
	if bands <= 0 {
		bands = DefaultBands
	}
	maxAnswerLen := opts.MaxAnswerLen
	if maxAnswerLen <= 0 {
		maxAnswerLen = DefaultMaxAnswerLen
	}
	return &Cache{
		opts: options{
			vectorize:    opts.Vectorize,
			similarity:   opts.Similarity,
			threshold:    threshold,
			ttl:          ttl,
			maxEntries:   maxEntries,
			bands:        bands,
			maxAnswerLen: maxAnswerLen,
			clock:        opts.Clock,
			tokenCount:   opts.TokenCount,
			filter:       opts.Filter,
			rewrite:      opts.Rewrite,
			warn:         opts.Warn,
		},
		skip:          skip,
		useBand:       opts.Similarity == nil,
		entries:       make(map[int64]*entry),
		byFingerprint: make(map[string]map[int64]struct{}),
		bands:         make(map[string]map[int64]struct{}),
		disabled:      make(map[string]bool),
	}, nil
}

// compileSkip 合并关键词与正则跳过规则。
func compileSkip(words, patterns []string) (*regexp.Regexp, error) {
	list := words
	if list == nil {
		list = defaultSkipList()
	}
	parts := make([]string, 0, len(list)+len(patterns))
	for _, w := range list {
		if strings.TrimSpace(w) == "" {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(w))
	}
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	re, err := regexp.Compile(strings.Join(parts, "|"))
	if err != nil {
		return nil, fmt.Errorf("semcache: compile skip patterns: %w", err)
	}
	return re, nil
}

// defaultSkipList 返回内置的易变话题关键词。用函数而非包级变量：避免可变全局状态。
func defaultSkipList() []string {
	return []string{
		"天气", "气温", "温度", "今天", "明天", "现在", "几点", "日期",
		"股价", "汇率", "比分", "weather", "temperature", "today", "tomorrow", "now",
	}
}

// HammingSimilarity 是默认相似度：1 - 汉明距离/公共位长。
func HammingSimilarity(a, b vector.Binary) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}
	d := vector.Hamming(a, b)
	return 1 - float64(d)/float64(n*8)
}

// Get 返回缓存答案；命中要求上下文指纹一致且相似度达标。
func (c *Cache) Get(question, fingerprint string) (string, bool) {
	if c == nil {
		return "", false
	}
	q := strings.TrimSpace(question)
	fp := strings.TrimSpace(fingerprint)
	if q == "" || fp == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled[fp] {
		c.stats.Misses++
		return "", false
	}
	if c.skip != nil && c.skip.MatchString(q) {
		c.stats.Skipped++
		c.stats.Misses++
		return "", false
	}
	key := c.keyFor(q)
	now := c.now()
	best := c.findBestLocked(fp, key, q, now)
	if best == nil {
		c.stats.Misses++
		return "", false
	}
	best.usedAt = now
	best.hits++
	c.stats.Hits++
	c.stats.TokensSaved += best.tokens
	ans := best.answer
	if c.opts.rewrite != nil {
		ans = c.opts.rewrite(ans)
	}
	return ans, true
}

// findBestLocked 在候选桶里找指纹一致、相似度最高的未过期条目；调用方持锁。
func (c *Cache) findBestLocked(fp string, key vector.Binary, question string, now time.Time) *entry {
	var best *entry
	bestSim := 0.0
	for id := range c.candidatesLocked(fp, key, question) {
		e := c.entries[id]
		if e == nil || e.fingerprint != fp {
			continue
		}
		if c.expired(e, now) {
			c.removeLocked(e)
			c.stats.Expirations++
			continue
		}
		var sim float64
		if c.opts.vectorize == nil {
			if e.question != question {
				continue
			}
			sim = 1
		} else {
			sim = c.similarity(key, e.key)
			if sim < c.opts.threshold {
				continue
			}
		}
		if best == nil || sim > bestSim || (sim == bestSim && e.id < best.id) {
			best, bestSim = e, sim
		}
	}
	return best
}

// Put 缓存一条"问题 -> 回答"；跳过列表、过滤链或长度上限会拒绝写入。
func (c *Cache) Put(question, fingerprint, answer string) {
	if c == nil {
		return
	}
	q := strings.TrimSpace(question)
	fp := strings.TrimSpace(fingerprint)
	if q == "" || fp == "" || strings.TrimSpace(answer) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled[fp] {
		return
	}
	if c.skip != nil && c.skip.MatchString(q) {
		c.stats.Skipped++
		return
	}
	if c.opts.maxAnswerLen > 0 && len([]rune(answer)) > c.opts.maxAnswerLen {
		c.stats.Rejected++
		c.warnf("semcache: answer exceeds max length; not cached")
		return
	}
	if c.opts.filter != nil {
		filtered, ok := c.opts.filter(answer)
		if !ok {
			c.stats.Rejected++
			c.warnf("semcache: answer rejected by exit filter; not cached")
			return
		}
		answer = filtered
	}
	if strings.TrimSpace(answer) == "" {
		c.stats.Rejected++
		return
	}
	now := c.now()
	key := c.keyFor(q)
	if existing := c.findExactLocked(fp, q); existing != nil {
		c.removeLocked(existing)
	}
	c.evictLocked()
	c.nextID++
	e := &entry{
		id:          c.nextID,
		key:         slices.Clone(key),
		fingerprint: fp,
		question:    q,
		answer:      answer,
		createdAt:   now,
		usedAt:      now,
		tokens:      c.tokenCount(answer),
	}
	c.entries[e.id] = e
	c.indexLocked(e)
}

// findExactLocked 按 (指纹, 问题原文) 查找已有条目。
func (c *Cache) findExactLocked(fp, question string) *entry {
	for id := range c.byFingerprint[fp] {
		e := c.entries[id]
		if e != nil && e.question == question {
			return e
		}
	}
	return nil
}

// candidatesLocked 返回可能命中的条目集合：精确模式按问题原文，向量模式按位带。
func (c *Cache) candidatesLocked(fp string, key vector.Binary, question string) map[int64]struct{} {
	if c.opts.vectorize == nil {
		out := make(map[int64]struct{})
		for id := range c.byFingerprint[fp] {
			if e := c.entries[id]; e != nil && e.question == question {
				out[id] = struct{}{}
			}
		}
		return out
	}
	if c.useBand && bandsSafe(key, c.opts.threshold) {
		out := make(map[int64]struct{})
		for _, bk := range c.bandKeys(fp, key) {
			for id := range c.bands[bk] {
				out[id] = struct{}{}
			}
		}
		return out
	}
	return c.byFingerprint[fp]
}

// keyFor 计算问题向量；未注入 Vectorize 时返回 nil。
func (c *Cache) keyFor(question string) vector.Binary {
	if c.opts.vectorize == nil {
		return nil
	}
	return c.opts.vectorize(question)
}

// similarity 计算两条二值向量的相似度。
func (c *Cache) similarity(a, b vector.Binary) float64 {
	if c.opts.similarity != nil {
		return c.opts.similarity(a, b)
	}
	return HammingSimilarity(a, b)
}

// bandsSafe 判断在给定阈值下位带索引是否仍有鸽巢保证。
func bandsSafe(key vector.Binary, threshold float64) bool {
	if len(key) == 0 {
		return false
	}
	bits := len(key) * 8
	maxDist := int(math.Floor((1 - threshold) * float64(bits)))
	return maxDist < len(key)
}

// bandKeys 把向量按字节等分成多个位带，返回 (指纹, 位带) 复合键。
func (c *Cache) bandKeys(fingerprint string, key vector.Binary) []string {
	if len(key) == 0 || c.opts.bands <= 0 {
		return nil
	}
	n := c.opts.bands
	if n > len(key) {
		n = len(key)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		start := i * len(key) / n
		end := (i + 1) * len(key) / n
		if end <= start {
			continue
		}
		out = append(out, fingerprint+"\x00"+strconv.Itoa(i)+"\x00"+string(key[start:end]))
	}
	return out
}

// indexLocked 把条目加入指纹桶与位带索引；调用方持锁。
func (c *Cache) indexLocked(e *entry) {
	set := c.byFingerprint[e.fingerprint]
	if set == nil {
		set = make(map[int64]struct{})
		c.byFingerprint[e.fingerprint] = set
	}
	set[e.id] = struct{}{}
	if !c.useBand || !bandsSafe(e.key, c.opts.threshold) {
		return
	}
	for _, bk := range c.bandKeys(e.fingerprint, e.key) {
		b := c.bands[bk]
		if b == nil {
			b = make(map[int64]struct{})
			c.bands[bk] = b
		}
		b[e.id] = struct{}{}
	}
}

// removeLocked 从存储与全部索引中一致移除条目；调用方持锁。
func (c *Cache) removeLocked(e *entry) {
	delete(c.entries, e.id)
	if set := c.byFingerprint[e.fingerprint]; set != nil {
		delete(set, e.id)
		if len(set) == 0 {
			delete(c.byFingerprint, e.fingerprint)
		}
	}
	if !c.useBand || !bandsSafe(e.key, c.opts.threshold) {
		return
	}
	for _, bk := range c.bandKeys(e.fingerprint, e.key) {
		if b := c.bands[bk]; b != nil {
			delete(b, e.id)
			if len(b) == 0 {
				delete(c.bands, bk)
			}
		}
	}
}

// expired 判断条目是否超过 TTL。
func (c *Cache) expired(e *entry, now time.Time) bool {
	return c.opts.ttl > 0 && now.Sub(e.createdAt) >= c.opts.ttl
}

// evictLocked 在容量超限时反复淘汰，直到留出空位。
func (c *Cache) evictLocked() {
	for len(c.entries) >= c.opts.maxEntries {
		victim := c.victimLocked()
		if victim == nil {
			return
		}
		c.removeLocked(victim)
		c.stats.Evictions++
	}
}

// victimLocked 选出淘汰对象：命中次数最少优先，其次最久未使用，最后 ID 最小。
// 这同时满足 F-63 的"低命中条目优先淘汰"与 LRU 语义，且顺序确定。
func (c *Cache) victimLocked() *entry {
	var victim *entry
	for _, e := range c.entries {
		switch {
		case victim == nil:
			victim = e
		case e.hits < victim.hits:
			victim = e
		case e.hits == victim.hits && e.usedAt.Before(victim.usedAt):
			victim = e
		case e.hits == victim.hits && e.usedAt.Equal(victim.usedAt) && e.id < victim.id:
			victim = e
		}
	}
	return victim
}

// SetEnabled 按会话/人格启用或禁用缓存；禁用会同时清空该指纹的条目。
func (c *Cache) SetEnabled(fingerprint string, enabled bool) {
	if c == nil {
		return
	}
	fp := strings.TrimSpace(fingerprint)
	if fp == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if enabled {
		delete(c.disabled, fp)
		return
	}
	c.disabled[fp] = true
	c.deleteFingerprintLocked(fp)
}

// Enabled 报告某指纹是否启用缓存。
func (c *Cache) Enabled(fingerprint string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.disabled[strings.TrimSpace(fingerprint)]
}

// Delete 移除某指纹的全部条目，但不改变启用状态。
func (c *Cache) Delete(fingerprint string) {
	if c == nil {
		return
	}
	fp := strings.TrimSpace(fingerprint)
	if fp == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteFingerprintLocked(fp)
}

// deleteFingerprintLocked 移除指定指纹的全部条目；调用方持锁。
func (c *Cache) deleteFingerprintLocked(fp string) {
	for id := range c.byFingerprint[fp] {
		if e := c.entries[id]; e != nil {
			c.removeLocked(e)
		}
	}
}

// Clear 清空全部条目与禁用状态。
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[int64]*entry)
	c.byFingerprint = make(map[string]map[int64]struct{})
	c.bands = make(map[string]map[int64]struct{})
	c.disabled = make(map[string]bool)
}

// Purge 移除全部过期条目，返回移除数。
func (c *Cache) Purge() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	removed := 0
	for _, e := range c.entries {
		if c.expired(e, now) {
			c.removeLocked(e)
			c.stats.Expirations++
			removed++
		}
	}
	return removed
}

// Len 返回当前条目数。
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats 返回当前计数快照，并回填 Entries。
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries = len(c.entries)
	return s
}

// now 返回当前时间，测试可注入确定性时钟。
func (c *Cache) now() time.Time {
	if c.opts.clock != nil {
		return c.opts.clock()
	}
	return time.Now()
}

// tokenCount 估算答案 token 数。
func (c *Cache) tokenCount(answer string) int {
	if c.opts.tokenCount != nil {
		return c.opts.tokenCount(answer)
	}
	return len([]rune(answer))
}

// warnf 在配置了 Warn 时记录一条告警。
func (c *Cache) warnf(msg string) {
	if c.opts.warn != nil {
		c.opts.warn(msg)
	}
}
