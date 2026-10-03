// Package vector 提供二值向量编码与分桶汉明距离检索（F-50）。
//
// 设计取舍：
//   - 二值量化 b[i] = (v[i] >= vtb) ? 1 : 0（vtb 默认 0），按 8 位一字节打包成
//     Binary（即 []byte），64 维恰好 8 字节，可直接作为 SQLite 的 BLOB 落库。
//   - 分桶 group = round(‖v‖ × k) mod n（k 默认 2.0、n 默认 64）。查询只扫描
//     与查询同桶以及相邻桶的候选，把比较次数从 N 降到约 N/n×(2r+1)。
//     默认取"同桶 + 相邻"（半径 r=1）而非仅同桶：仅同桶会漏掉恰好跨过桶边界的
//     近邻，召回率不稳；半径 1 是召回率与比较次数之间的折中，代价是候选数约为
//     同桶的 3 倍。需要时可用 SearchRadius 调整为仅同桶或更宽的半径。
//   - 距离逐字节用 math/bits.OnesCount8 计算，热循环无堆分配。
//   - 检索结果是 []Hit，排序时 ID/Text/Distance 在同一结构体内整体移动，
//     从类型上杜绝"并行数组分开排序"导致的文本与 ID 错位（F-50 头号易错点）。
//
// 并发模型：Index 内部用 sync.RWMutex 保护；Search 持读锁，故可并发查询。
package vector

import (
	"math"
	"math/bits"
	"slices"
	"sync"
)

// Binary 是打包的二值向量：第 i 位（i 从 0 起）存放在第 i/8 个字节的
// 第 i%8 位。底层类型是 []byte，因此可直接写入/读出 BLOB。
type Binary []byte

// Encode 按默认阈值 vtb=0 把浮点向量量化为二值向量：
// v[i] >= 0 置 1，否则置 0。空输入返回 nil。
func Encode(v []float32) Binary { return EncodeThreshold(v, 0) }

// EncodeThreshold 按给定阈值 vtb 量化：b[i] = (v[i] >= vtb) ? 1 : 0。
// 打包为 []byte，维度不足一字节的高位补零。空输入返回 nil。
func EncodeThreshold(v []float32, vtb float32) Binary {
	if len(v) == 0 {
		return nil
	}
	b := make(Binary, (len(v)+7)/8)
	for i, x := range v {
		if x >= vtb {
			b[i/8] |= 1 << uint(i%8)
		}
	}
	return b
}

// FromBytes 从打包字节构造 Binary，并复制一份，调用方后续修改 b 不影响结果。
// 空输入返回 nil。
func FromBytes(b []byte) Binary {
	if len(b) == 0 {
		return nil
	}
	return Binary(slices.Clone(b))
}

// Bytes 返回打包字节的副本，便于直接写入 BLOB 或参与序列化。
// 空向量返回 nil。
func (b Binary) Bytes() []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}

// Len 返回向量的位数（字节数 × 8）。
func (b Binary) Len() int { return len(b) * 8 }

// Hamming 返回两个二值向量的汉明距离，逐字节用 bits.OnesCount8 累加。
//
// 长度不等时只比较公共前缀（较短一方覆盖的字节），文档约定调用方应保证
// 维度一致；同维向量才得到完整的位数距离。
func Hamming(a, b Binary) int {
	n := min(len(a), len(b))
	d := 0
	for i := 0; i < n; i++ {
		d += bits.OnesCount8(a[i] ^ b[i])
	}
	return d
}

// Norm 返回浮点向量的 L2 范数 ‖v‖。
func Norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

// Config 是二值化与分桶检索的参数。零值经归一化后等价于默认配置。
type Config struct {
	VTB         float32 // 二值化阈值，默认 0
	K           float64 // 分桶系数，默认 2.0（<=0 时取默认）
	N           int     // 桶数量，默认 64（<=0 时取默认）
	MaxDistance int     // 结果距离上限，默认 8；<0 表示不设上限
}

// DefaultConfig 返回规格默认值：VTB=0、K=2.0、N=64、MaxDistance=8。
func DefaultConfig() Config {
	return Config{VTB: 0, K: 2.0, N: 64, MaxDistance: 8}
}

// normalized 回填零值字段，使零值 Config 也安全可用。
func (c Config) normalized() Config {
	if c.K <= 0 {
		c.K = 2.0
	}
	if c.N <= 0 {
		c.N = 64
	}
	switch {
	case c.MaxDistance == 0:
		c.MaxDistance = 8
	case c.MaxDistance < 0:
		c.MaxDistance = math.MaxInt
	}
	return c
}

// Group 由范数与分桶参数计算桶号：round(norm×k) mod n，结果落在 [0,n)。
func Group(norm, k float64, n int) int {
	if k <= 0 {
		k = 2.0
	}
	if n <= 0 {
		n = 64
	}
	g := int(math.Mod(math.Round(norm*k), float64(n)))
	if g < 0 {
		g += n
	}
	return g
}

// Entry 是索引中的一条记录，字段与持久化表
// (id, text, vector BLOB, norm REAL, group_id INT) 一一对应，可直接落库/回读。
type Entry struct {
	ID     int64
	Text   string
	Vector Binary  // BLOB
	Norm   float64 // REAL
	Group  int     // INT
}

// NewEntry 用配置把浮点向量编码成可直接落库的 Entry，并计算出 norm 与 group，
// 保证三者一致。
func (c Config) NewEntry(id int64, text string, v []float32) Entry {
	c = c.normalized()
	norm := Norm(v)
	return Entry{
		ID:     id,
		Text:   text,
		Vector: EncodeThreshold(v, c.VTB),
		Norm:   norm,
		Group:  Group(norm, c.K, c.N),
	}
}

// Hit 是一条检索结果。ID/Text/Distance 同属一个结构体，排序时整体移动。
type Hit struct {
	ID       int64
	Text     string
	Distance int
}

// Stats 描述一次检索的规模，用于验证分桶确实减少了比较次数。
type Stats struct {
	Buckets    int // 实际扫描的非空桶数
	Candidates int // 实际参与汉明距离计算的候选数
	Matched    int // 距离不超过阈值的结果数（截断为 Top-k 之前）
	Returned   int // 最终返回的条数
}

// Index 是内存中的二值向量集合，支持并发 Add/Remove/Search；零值可直接使用。
type Index struct {
	mu      sync.RWMutex
	cfg     Config
	entries map[int64]Entry
	buckets map[int]map[int64]struct{}
}

// NewIndex 用默认配置返回一个空索引。
func NewIndex() *Index { return NewIndexWithConfig(DefaultConfig()) }

// NewIndexWithConfig 用给定配置返回一个空索引，零值字段自动回填默认值。
func NewIndexWithConfig(cfg Config) *Index {
	return &Index{
		cfg:     cfg.normalized(),
		entries: make(map[int64]Entry),
		buckets: make(map[int]map[int64]struct{}),
	}
}

// Add 用配置把 v 编码后写入 id，同时计算 norm 与 group，保证与向量一致；
// 重复 id 覆盖旧记录并维护桶索引。文本用于结果回显。
func (i *Index) Add(id int64, text string, v []float32) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensure()
	i.put(i.cfg.normalized().NewEntry(id, text, v))
}

// AddEntry 写入一条已编码的记录（典型场景：从数据库回读）。group 会依据
// norm 与配置重新推导，保证落库后 group 与 norm 一致；norm 被信任。
func (i *Index) AddEntry(e Entry) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensure()
	cfg := i.cfg.normalized()
	e.Vector = FromBytes(e.Vector)
	e.Group = Group(e.Norm, cfg.K, cfg.N)
	i.put(e)
}

// ensure 惰性初始化零值 Index 的内部结构。
func (i *Index) ensure() {
	if i.entries == nil {
		i.entries = make(map[int64]Entry)
	}
	if i.buckets == nil {
		i.buckets = make(map[int]map[int64]struct{})
	}
}

// put 在调用方持写锁的前提下写入记录并同步桶索引。
func (i *Index) put(e Entry) {
	if old, ok := i.entries[e.ID]; ok && old.Group != e.Group {
		if b := i.buckets[old.Group]; b != nil {
			delete(b, e.ID)
			if len(b) == 0 {
				delete(i.buckets, old.Group)
			}
		}
	}
	i.entries[e.ID] = e
	b := i.buckets[e.Group]
	if b == nil {
		b = make(map[int64]struct{})
		i.buckets[e.Group] = b
	}
	b[e.ID] = struct{}{}
}

// Remove 删除 id 对应的记录，返回是否真的删除了条目。
func (i *Index) Remove(id int64) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	e, ok := i.entries[id]
	if !ok {
		return false
	}
	delete(i.entries, id)
	if b := i.buckets[e.Group]; b != nil {
		delete(b, id)
		if len(b) == 0 {
			delete(i.buckets, e.Group)
		}
	}
	return true
}

// Len 返回索引中的记录条数。
func (i *Index) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.entries)
}

// Get 返回 id 对应的记录副本。向量被复制，调用方修改不影响索引。
func (i *Index) Get(id int64) (Entry, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.entries[id]
	if !ok {
		return Entry{}, false
	}
	e.Vector = FromBytes(e.Vector)
	return e, true
}

// Search 返回与 q 汉明距离不超过阈值（默认 8）的至多 k 条结果，按距离升序；
// 距离并列时按 ID 升序，保证确定性。只扫描同桶与相邻桶（半径 1）。
//
// k<=0、空查询或空索引时返回 nil。
func (i *Index) Search(q []float32, k int) []Hit {
	hits, _ := i.search(q, k, 1)
	return hits
}

// SearchRadius 同 Search，但显式指定相邻桶半径：0 表示仅同桶，r>=1 表示
// 左右各扩 r 个桶。半径越大召回越全、比较越多。
func (i *Index) SearchRadius(q []float32, k, radius int) []Hit {
	hits, _ := i.search(q, k, radius)
	return hits
}

// SearchWithStats 同 Search，并返回本次检索的候选数等统计，便于验证分桶效果。
func (i *Index) SearchWithStats(q []float32, k int) ([]Hit, Stats) {
	return i.search(q, k, 1)
}

// search 是检索主逻辑。结果排序在单个 []Hit 上完成，ID/Text 不会错位。
func (i *Index) search(q []float32, k, radius int) ([]Hit, Stats) {
	var st Stats
	if k <= 0 || len(q) == 0 {
		return nil, st
	}
	cfg := i.cfg.normalized()
	qb := EncodeThreshold(q, cfg.VTB)
	if len(qb) == 0 {
		return nil, st
	}
	qg := Group(Norm(q), cfg.K, cfg.N)

	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(i.entries) == 0 {
		return nil, st
	}

	// 用单一 []Hit 维持 Top-k（有界插入），排序与截断同时完成，
	// ID/Text/Distance 整体移动，不会出现并行数组错位。
	hits := make([]Hit, 0, min(k, len(i.entries)))
	for _, g := range neighborGroups(qg, cfg.N, radius) {
		b := i.buckets[g]
		if len(b) == 0 {
			continue
		}
		st.Buckets++
		for id := range b {
			e, ok := i.entries[id]
			if !ok {
				continue
			}
			st.Candidates++
			d := Hamming(qb, e.Vector)
			if d > cfg.MaxDistance {
				continue
			}
			st.Matched++
			hits = insertHit(hits, Hit{ID: e.ID, Text: e.Text, Distance: d}, k)
		}
	}
	if len(hits) == 0 {
		return nil, st
	}
	st.Returned = len(hits)
	return hits, st
}

// insertHit 把 h 插入已按 (Distance, ID) 升序的 best，并保持长度不超过 k。
// 唯一的有序容器就是 []Hit，排序时 Text/ID 与 Distance 一起移动。
func insertHit(best []Hit, h Hit, k int) []Hit {
	if len(best) == k {
		if !lessHit(h, best[k-1]) {
			return best
		}
		best = best[:k-1]
	}
	pos := len(best)
	for pos > 0 && lessHit(h, best[pos-1]) {
		pos--
	}
	best = append(best, Hit{})
	copy(best[pos+1:], best[pos:])
	best[pos] = h
	return best
}

// lessHit 是结果的全序：先比距离，再比 ID，保证并列时顺序确定。
func lessHit(a, b Hit) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	return a.ID < b.ID
}

// neighborGroups 返回以 g 为中心、左右各 radius 个桶（含自身）的桶号列表；
// 桶号按 n 环绕。半径过大时不重复枚举，直接返回全部 n 个桶。
func neighborGroups(g, n, radius int) []int {
	if n <= 0 {
		n = 64
	}
	if radius < 0 {
		radius = 0
	}
	if 2*radius+1 >= n {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all
	}
	out := make([]int, 0, 2*radius+1)
	for d := -radius; d <= radius; d++ {
		out = append(out, ((g+d)%n+n)%n)
	}
	return out
}
