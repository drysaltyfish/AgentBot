package imagehash

import "sync"

// Deduper 是一个有界、并发安全的近似哈希去重器。
//
// 它用"分桶"索引避免全量线性扫描：把 64 位哈希切成 threshold+1 个互不重叠的
// 位块，每个条目在每个块下各索引一次。由鸽巢原理，两个哈希的汉明距离不超过
// threshold 时，至少有一个块完全相同，因此只需检查同桶候选即可，不会漏判。
// 当距离为 0 时用精确表直接判定。
type Deduper struct {
	mu         sync.Mutex
	threshold  int
	maxEntries int
	blocks     int
	hashes     map[Hash]struct{}
	buckets    map[uint64]map[Hash]struct{}
	order      []Hash
}

// NewDeduper 构造去重器：threshold 为命中所需的最大汉明距离（钳到 0..64），
// maxEntries 为容量上限（<=0 时不保留任何条目）。超过容量时淘汰最旧的条目。
func NewDeduper(threshold, maxEntries int) *Deduper {
	if threshold < 0 {
		threshold = 0
	}
	if threshold > hashBits {
		threshold = hashBits
	}
	blocks := threshold + 1
	if blocks > hashBits {
		blocks = hashBits
	}
	if blocks < 1 {
		blocks = 1
	}
	return &Deduper{
		threshold:  threshold,
		maxEntries: maxEntries,
		blocks:     blocks,
		hashes:     make(map[Hash]struct{}),
		buckets:    make(map[uint64]map[Hash]struct{}),
		order:      make([]Hash, 0, max(0, maxEntries)),
	}
}

// bucketKeys 返回 h 在各分块下的桶键。块按低位到高位等分；块数 <=1 时退化为
// 单桶（线性），键固定为 0。键的高位编码块序号，低位为块内取值。
func bucketKeys(h Hash, blocks int) []uint64 {
	if blocks <= 1 {
		return []uint64{0}
	}
	keys := make([]uint64, blocks)
	var shift uint
	for i := 0; i < blocks; i++ {
		size := hashBits / blocks
		if i < hashBits%blocks {
			size++
		}
		mask := uint64(1)<<uint(size) - 1
		keys[i] = uint64(i)<<48 | (uint64(h)>>shift)&mask
		shift += uint(size)
	}
	return keys
}

// Seen 判断 h 是否与已有条目近似（汉明距离 <= threshold）。
// 命中返回 true 且不改动状态；未命中则记入并返回 false。
func (d *Deduper) Seen(h Hash) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.hashes[h]; ok {
		return true
	}
	if d.maxEntries <= 0 {
		return false
	}
	// threshold 达到 64 时任意两个哈希都算命中，分桶不再适用（可能无公共块）。
	if d.threshold >= hashBits {
		if len(d.hashes) > 0 {
			return true
		}
	} else {
		for _, key := range bucketKeys(h, d.blocks) {
			for cand := range d.buckets[key] {
				if Distance(cand, h) <= d.threshold {
					return true
				}
			}
		}
	}
	d.insert(h)
	return false
}

// Len 返回当前条目数，恒不超过 maxEntries。
func (d *Deduper) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.hashes)
}

// insert 记录 h 并从所有桶索引中淘汰最旧条目，直到不超过容量。
func (d *Deduper) insert(h Hash) {
	d.hashes[h] = struct{}{}
	for _, key := range bucketKeys(h, d.blocks) {
		m := d.buckets[key]
		if m == nil {
			m = make(map[Hash]struct{})
			d.buckets[key] = m
		}
		m[h] = struct{}{}
	}
	d.order = append(d.order, h)
	for len(d.order) > d.maxEntries {
		old := d.order[0]
		d.order = d.order[1:]
		d.remove(old)
	}
}

// remove 从精确表与全部桶中删除 h。
func (d *Deduper) remove(h Hash) {
	delete(d.hashes, h)
	for _, key := range bucketKeys(h, d.blocks) {
		m := d.buckets[key]
		delete(m, h)
		if len(m) == 0 {
			delete(d.buckets, key)
		}
	}
}
