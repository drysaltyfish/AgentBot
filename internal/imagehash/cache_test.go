package imagehash

import (
	"bytes"
	"image/jpeg"
	"sync"
	"testing"
	"time"
)

// spy 统计"视觉模型调用"次数，验证命中后跳过重算。
type descSpy struct {
	calls int
}

// describe 模拟视觉模型：仅在缓存未命中时被调用。
func (s *descSpy) describe() string {
	s.calls++
	return "一只猫"
}

// Test_F62_CacheSecondLookupHits 同一图片两次入站：第二次命中且不重算。
func Test_F62_CacheSecondLookupHits(t *testing.T) {
	c := NewCache(CacheOptions{})
	h := PerceptualHash(patternImage(128, 128))
	spy := &descSpy{}

	desc, ok := c.Lookup(h)
	if ok {
		t.Fatalf("first lookup unexpectedly hit: %q", desc)
	}
	c.Store(h, spy.describe())

	desc, ok = c.Lookup(h)
	if !ok {
		t.Fatal("second lookup must hit")
	}
	if desc != "一只猫" {
		t.Fatalf("desc = %q, want 一只猫", desc)
	}
	if spy.calls != 1 {
		t.Fatalf("visual model calls = %d, want 1 (second lookup must skip)", spy.calls)
	}
}

// Test_F62_CacheScaledAndCompressedHit 缩放/轻微压缩后的同图仍命中。
func Test_F62_CacheScaledAndCompressedHit(t *testing.T) {
	base := patternImage(160, 160)
	hBase := PerceptualHash(base)

	c := NewCache(CacheOptions{})
	c.Store(hBase, "图案")

	// 缩放：同一图案的不同分辨率。
	scaled := PerceptualHash(patternImage(64, 64))
	if d := Distance(hBase, scaled); d > DefaultThreshold {
		t.Fatalf("scaled distance = %d, 超出默认阈值 %d（测试前提不成立）", d, DefaultThreshold)
	}
	if _, ok := c.Lookup(scaled); !ok {
		t.Fatalf("scaled variant (distance %d) must hit", Distance(hBase, scaled))
	}

	// 轻微压缩：JPEG 编解码往返。
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, base, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	dec, err := jpeg.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	compressed := PerceptualHash(dec)
	if d := Distance(hBase, compressed); d > DefaultThreshold {
		t.Fatalf("compressed distance = %d, 超出默认阈值 %d（测试前提不成立）", d, DefaultThreshold)
	}
	if _, ok := c.Lookup(compressed); !ok {
		t.Fatalf("compressed variant (distance %d) must hit", Distance(hBase, compressed))
	}
}

// Test_F62_CacheThresholdConfigurable 阈值可配置：同一对哈希在不同阈值下结果不同。
func Test_F62_CacheThresholdConfigurable(t *testing.T) {
	h := Hash(0x1234_5678_9ABC_DEF0)
	near := Hash(uint64(h) ^ 0b1111) // 距离 4

	lo := NewCache(CacheOptions{Threshold: 2})
	lo.Store(h, "d")
	if _, ok := lo.Lookup(near); ok {
		t.Fatal("distance 4 must miss with threshold 2")
	}

	hi := NewCache(CacheOptions{Threshold: 4})
	hi.Store(h, "d")
	if _, ok := hi.Lookup(near); !ok {
		t.Fatal("distance 4 must hit with threshold 4")
	}
}

// Test_F62_CacheTTLExpiry TTL 到期不再命中；TTL 可配置。
func Test_F62_CacheTTLExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(CacheOptions{TTL: time.Hour, Now: func() time.Time { return now }})
	h := Hash(0xABCD)

	c.Store(h, "描述")
	now = now.Add(59 * time.Minute)
	if _, ok := c.Lookup(h); !ok {
		t.Fatal("must hit before TTL expiry")
	}

	now = now.Add(2 * time.Minute) // 累计 61 分钟 > 1 小时
	if _, ok := c.Lookup(h); ok {
		t.Fatal("must miss after TTL expiry")
	}
	if got := c.Len(); got != 0 {
		t.Fatalf("Len after expiry = %d, want 0 (过期条目应被清除)", got)
	}
}

// Test_F62_CacheTTLPurgeOnStore 过期条目在 Store 时也被惰性清除，不挤占容量。
func Test_F62_CacheTTLPurgeOnStore(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(CacheOptions{TTL: time.Minute, MaxEntries: 2, Now: func() time.Time { return now }})
	c.Store(Hash(1), "a")
	c.Store(Hash(2), "b")
	now = now.Add(2 * time.Minute)
	c.Store(Hash(3), "c")
	if got := c.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 (两条过期条目应先被清除)", got)
	}
}

// Test_F62_CacheLRUEviction 容量上限触发 LRU 淘汰：最久未用者先出。
func Test_F62_CacheLRUEviction(t *testing.T) {
	// 默认阈值 8 下这四者两两距离均 >8，故只可能精确命中，便于断言淘汰。
	c := NewCache(CacheOptions{MaxEntries: 3})
	h := []Hash{
		0x0000_0000_0000_0000,
		0x00FF_00FF_00FF_00FF,
		0xFF00_FF00_FF00_FF00,
		0xFFFF_FFFF_FFFF_FFFF,
	}
	for i := 0; i < 3; i++ {
		c.Store(h[i], "d")
	}
	// 命中 h[0]，使其成为最近使用；此时最久未用是 h[1]。
	if _, ok := c.Lookup(h[0]); !ok {
		t.Fatal("h0 must be present before eviction")
	}
	c.Store(h[3], "d")

	if got := c.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	if _, ok := c.Lookup(h[1]); ok {
		t.Fatal("least recently used h1 must be evicted")
	}
	for _, i := range []int{0, 2, 3} {
		if _, ok := c.Lookup(h[i]); !ok {
			t.Fatalf("h%d must survive eviction", i)
		}
	}
}

// Test_F62_CacheBucketing 分桶生效：查询比较次数远小于条目数。
func Test_F62_CacheBucketing(t *testing.T) {
	const n = 8000
	c := NewCache(CacheOptions{MaxEntries: 2 * n})

	var x uint64 = 0x9E37_79B9_7F4A_7C15
	seen := make(map[Hash]struct{}, n)
	for len(seen) < n {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		h := Hash(x)
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		c.Store(h, "d")
	}

	// 取一个不在集合中的新哈希作为探针。
	var probe Hash
	for {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		probe = Hash(x)
		if _, dup := seen[probe]; !dup {
			break
		}
	}

	c.ResetComparisons()
	if _, ok := c.Lookup(probe); ok {
		t.Fatal("fresh probe unexpectedly hit")
	}
	got := c.Comparisons()
	if got == 0 {
		t.Fatal("expected bucket candidate comparisons, got 0")
	}
	if got >= n/8 {
		t.Fatalf("comparisons = %d, want << %d entries (bucketing ineffective)", got, n)
	}
	t.Logf("bucketing: %d comparisons for %d entries", got, n)
}

// Test_F62_CacheNearMatchRecall 分桶不得漏判：距离恰好等于阈值的近邻仍命中。
func Test_F62_CacheNearMatchRecall(t *testing.T) {
	const threshold = 8
	c := NewCache(CacheOptions{Threshold: threshold, MaxEntries: 4096})

	var x uint64 = 0x1234_5678_9ABC_DEF0
	for i := 0; i < 2000; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		base := Hash(x)
		c.Store(base, "d")

		near := base
		for b := 0; b < threshold; b++ {
			near ^= 1 << uint((i*7+b)%hashBits)
		}
		if _, ok := c.Lookup(near); !ok {
			t.Fatalf("near match (distance %d) missed at iteration %d", Distance(base, near), i)
		}
	}
}

// Test_F62_CacheDescTruncate 描述长度上限，按 rune 截断。
func Test_F62_CacheDescTruncate(t *testing.T) {
	c := NewCache(CacheOptions{MaxDescRunes: 5})
	h := Hash(42)
	c.Store(h, "一二三四五六七八九")

	got, ok := c.Lookup(h)
	if !ok {
		t.Fatal("must hit")
	}
	if got != "一二三四五" {
		t.Fatalf("desc = %q, want 一二三四五 (按 rune 截断)", got)
	}

	// 上限可配置：默认 512。
	d := NewCache(CacheOptions{})
	long := make([]rune, 600)
	for i := range long {
		long[i] = 'a'
	}
	d.Store(Hash(7), string(long))
	if got, _ := d.Lookup(Hash(7)); len([]rune(got)) != DefaultMaxDescRunes {
		t.Fatalf("default truncation = %d runes, want %d", len([]rune(got)), DefaultMaxDescRunes)
	}
}

// Test_F62_CacheConcurrent 并发 Lookup/Store 安全且容量受限。
func Test_F62_CacheConcurrent(t *testing.T) {
	const maxEntries = 64
	c := NewCache(CacheOptions{Threshold: 4, MaxEntries: maxEntries, TTL: time.Hour})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				h := Hash(uint64(g)<<32 | uint64(i))
				c.Store(h, "d")
				c.Lookup(h)
				c.Lookup(Hash(uint64(g)<<32 | uint64(i+1)))
			}
		}(g)
	}
	wg.Wait()

	if got := c.Len(); got > maxEntries {
		t.Fatalf("Len = %d exceeds MaxEntries %d", got, maxEntries)
	}
}

// Test_F62_SetDerivedDesc 段回写辅助只依赖 map，不导入 internal/event。
func Test_F62_SetDerivedDesc(t *testing.T) {
	seg := map[string]string{"type": "image"}
	if !SetDerivedDesc(seg, "  一只猫  ") {
		t.Fatal("SetDerivedDesc must report write")
	}
	if got := seg[DerivedDescKey]; got != "一只猫" {
		t.Fatalf("DerivedDescKey = %q, want 一只猫", got)
	}
	if SetDerivedDesc(seg, "   ") {
		t.Fatal("blank desc must not write")
	}
	if SetDerivedDesc(nil, "x") {
		t.Fatal("nil map must not write")
	}

	segments := map[int]map[string]string{
		0: {"type": "image"},
		1: {"type": "text"},
		2: nil,
	}
	if n := SetDerivedDescAll(segments, "dog"); n != 2 {
		t.Fatalf("SetDerivedDescAll wrote %d segments, want 2", n)
	}
	for _, i := range []int{0, 1} {
		if segments[i][DerivedDescKey] != "dog" {
			t.Fatalf("segment %d missing derived desc", i)
		}
	}
}

// Test_F62_LookupSegmentsWritesBack 命中后描述写回消息段，实现幂等。
func Test_F62_LookupSegmentsWritesBack(t *testing.T) {
	c := NewCache(CacheOptions{})
	h := Hash(0xDEAD_BEEF)
	c.Store(h, "截图")

	segments := map[int]map[string]string{
		0: {"type": "image"},
		1: {"type": "text"},
	}
	desc, ok := c.LookupSegments(h, segments)
	if !ok || desc != "截图" {
		t.Fatalf("LookupSegments = (%q,%v), want (截图,true)", desc, ok)
	}
	if segments[0][DerivedDescKey] != "截图" {
		t.Fatalf("image segment derived desc = %q, want 截图", segments[0][DerivedDescKey])
	}
}
