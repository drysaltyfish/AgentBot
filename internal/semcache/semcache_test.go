package semcache

import (
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/vector"
)

// testClock 是确定性时钟。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// f63vec 返回 64 维向量：默认全 1，diff 为真时最低位为 -1（与基准汉明距离 1）。
func f63vec(diff bool) []float32 {
	v := make([]float32, 64)
	for i := range v {
		v[i] = 1
	}
	if diff {
		v[0] = -1
	}
	return v
}

// mustNew 构造缓存，失败即终止。
func mustNew(t *testing.T, opts Options) *Cache {
	t.Helper()
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// askOnce 模拟一次问答：命中直接返回，未命中才"调用 LLM"并写入缓存。
func askOnce(t *testing.T, c *Cache, calls *int, question, fingerprint string) string {
	t.Helper()
	if ans, ok := c.Get(question, fingerprint); ok {
		return ans
	}
	*calls++
	ans := "answer:" + question
	c.Put(question, fingerprint, ans)
	return ans
}

// Test_F63_SecondIdenticalQuestionSkipsLLM 是核心验收：第二次直接命中，无 LLM 调用。
func Test_F63_SecondIdenticalQuestionSkipsLLM(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:  []string{},
	})
	calls := 0
	askOnce(t, c, &calls, "你是谁", "persona-A")
	askOnce(t, c, &calls, "你是谁", "persona-A")
	if calls != 1 {
		t.Fatalf("第二次必须命中缓存、不得调用 LLM: calls=%d", calls)
	}
	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("命中/未命中计数不对: %+v", stats)
	}
	if stats.TokensSaved == 0 {
		t.Fatalf("应累计节省 token: %+v", stats)
	}
}

// Test_F63_SimilarQuestionHits 验证相似度阈值生效（轻微改写也命中）。
func Test_F63_SimilarQuestionHits(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(q string) vector.Binary { return vector.Encode(f63vec(len(q) > 9)) },
		SkipList:  []string{},
	})
	calls := 0
	askOnce(t, c, &calls, "你是谁", "p")
	askOnce(t, c, &calls, "你是谁呀", "p")
	if calls != 1 {
		t.Fatalf("相似问题应命中: calls=%d", calls)
	}
}

// Test_F63_DifferentPersonaDoesNotHit 边界：按人格隔离，答案不串台。
func Test_F63_DifferentPersonaDoesNotHit(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:  []string{},
	})
	calls := 0
	askOnce(t, c, &calls, "你是谁", "persona-A")
	askOnce(t, c, &calls, "你是谁", "persona-B")
	if calls != 2 {
		t.Fatalf("不同人格不得命中: calls=%d", calls)
	}
}

// Test_F63_SkipListAvoidsCaching 边界：高波动话题不缓存。
func Test_F63_SkipListAvoidsCaching(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
	})
	calls := 0
	askOnce(t, c, &calls, "今天天气怎么样", "p")
	askOnce(t, c, &calls, "今天天气怎么样", "p")
	if calls != 2 {
		t.Fatalf("跳过列表命中的问题不得缓存: calls=%d", calls)
	}
	if c.Stats().Skipped == 0 {
		t.Fatalf("应记录跳过次数")
	}
}

// Test_F63_CustomSkipPattern 验证可配置正则过滤。
func Test_F63_CustomSkipPattern(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize:    func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:     []string{},
		SkipPatterns: []string{"实时.*"},
	})
	calls := 0
	askOnce(t, c, &calls, "实时汇率多少", "p")
	askOnce(t, c, &calls, "实时汇率多少", "p")
	if calls != 2 {
		t.Fatalf("正则跳过应生效: calls=%d", calls)
	}
	if _, err := New(Options{SkipPatterns: []string{"("}}); err == nil {
		t.Fatalf("非法正则应返回错误")
	}
}

// Test_F63_TTLExpiry 验证 TTL 过期。
func Test_F63_TTLExpiry(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:  []string{},
		TTL:       time.Minute,
		Clock:     clk.now,
	})
	calls := 0
	askOnce(t, c, &calls, "怎么用", "p")
	if _, ok := c.Get("怎么用", "p"); !ok {
		t.Fatalf("TTL 内应命中")
	}
	clk.advance(2 * time.Minute)
	askOnce(t, c, &calls, "怎么用", "p")
	if calls != 2 {
		t.Fatalf("过期后应未命中: calls=%d", calls)
	}
	if c.Stats().Expirations == 0 {
		t.Fatalf("应记录过期")
	}
}

// Test_F63_CapacityEvictsOldest 验证容量上限与 LRU 淘汰。
func Test_F63_CapacityEvictsOldest(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	c := mustNew(t, Options{
		SkipList:   []string{},
		MaxEntries: 2,
		Clock:      clk.now,
	})
	c.Put("q1", "p", "a1")
	clk.advance(time.Second)
	c.Put("q2", "p", "a2")
	clk.advance(time.Second)
	c.Put("q3", "p", "a3")
	if c.Len() != 2 {
		t.Fatalf("容量上限应为 2，得到 %d", c.Len())
	}
	if _, ok := c.Get("q1", "p"); ok {
		t.Fatalf("最旧的 q1 应被淘汰")
	}
	if _, ok := c.Get("q3", "p"); !ok {
		t.Fatalf("最新的 q3 应存在")
	}
	if c.Stats().Evictions != 1 {
		t.Fatalf("应记录一次淘汰: %+v", c.Stats())
	}
}

// Test_F63_LowHitEvictedFirst 验证低命中优先淘汰。
func Test_F63_LowHitEvictedFirst(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{SkipList: []string{}, MaxEntries: 2})
	c.Put("q1", "p", "a1")
	c.Put("q2", "p", "a2")
	if _, ok := c.Get("q2", "p"); !ok {
		t.Fatalf("q2 应命中")
	}
	c.Put("q3", "p", "a3")
	if _, ok := c.Get("q2", "p"); !ok {
		t.Fatalf("高命中的 q2 不应被淘汰")
	}
	if _, ok := c.Get("q1", "p"); ok {
		t.Fatalf("低命中的 q1 应被淘汰")
	}
}

// Test_F63_ExitFilterRejects 边界：答案未通过出口过滤链则不缓存。
func Test_F63_ExitFilterRejects(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		SkipList: []string{},
		Filter:   func(string) (string, bool) { return "", false },
	})
	c.Put("q", "p", "a")
	if c.Len() != 0 {
		t.Fatalf("被过滤的答案不得缓存: %d", c.Len())
	}
	if c.Stats().Rejected != 1 {
		t.Fatalf("应记录拒绝: %+v", c.Stats())
	}
}

// Test_F63_MaxAnswerLenRejects 边界：答案长度上限。
func Test_F63_MaxAnswerLenRejects(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{SkipList: []string{}, MaxAnswerLen: 3})
	c.Put("q", "p", "超过三个字符")
	if c.Len() != 0 {
		t.Fatalf("超长答案不得缓存: %d", c.Len())
	}
}

// Test_F63_SessionDisable 验证可按会话禁用。
func Test_F63_SessionDisable(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:  []string{},
	})
	c.SetEnabled("p", false)
	c.Put("你是谁", "p", "a")
	if _, ok := c.Get("你是谁", "p"); ok {
		t.Fatalf("禁用会话不得命中")
	}
	if c.Enabled("p") {
		t.Fatalf("Enabled 应为 false")
	}
	c.SetEnabled("p", true)
	c.Put("你是谁", "p", "a")
	if _, ok := c.Get("你是谁", "p"); !ok {
		t.Fatalf("重新启用后应命中")
	}
}

// Test_F63_RewriteOnHit 验证命中时可改写答案。
func Test_F63_RewriteOnHit(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		SkipList: []string{},
		Rewrite:  func(answer string) string { return answer + "[ctx]" },
	})
	c.Put("q", "p", "a")
	got, ok := c.Get("q", "p")
	if !ok || got != "a[ctx]" {
		t.Fatalf("命中应返回改写后的答案: %q ok=%v", got, ok)
	}
}

// Test_F63_StatsHitRateAndTokens 验证命中率与节省 token 指标。
func Test_F63_StatsHitRateAndTokens(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		SkipList:   []string{},
		TokenCount: func(string) int { return 10 },
	})
	c.Put("q", "p", "a")
	c.Get("q", "p")
	c.Get("missing", "p")
	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("计数不对: %+v", stats)
	}
	if stats.TokensSaved != 10 {
		t.Fatalf("节省 token 应为 10，得到 %d", stats.TokensSaved)
	}
	if stats.HitRate() != 0.5 {
		t.Fatalf("命中率应为 0.5，得到 %v", stats.HitRate())
	}
}

// Test_F63_ConcurrentAccess 验证并发安全（配合 -race 更有意义）。
func Test_F63_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := mustNew(t, Options{
		Vectorize: func(string) vector.Binary { return vector.Encode(f63vec(false)) },
		SkipList:  []string{},
	})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				q := "q" + string(rune('a'+id))
				c.Put(q, "p", "a")
				_, _ = c.Get(q, "p")
			}
		}(g)
	}
	wg.Wait()
	if c.Len() == 0 {
		t.Fatalf("并发写入后不应为空")
	}
}

// Test_F63_HammingSimilarity 单元验证默认相似度。
func Test_F63_HammingSimilarity(t *testing.T) {
	t.Parallel()
	a := vector.FromBytes([]byte{0x00})
	b := vector.FromBytes([]byte{0x00})
	if HammingSimilarity(a, b) != 1 {
		t.Fatalf("相同向量相似度应为 1")
	}
	diff := vector.FromBytes([]byte{0xFF})
	if got := HammingSimilarity(a, diff); got != 0 {
		t.Fatalf("全异向量相似度应为 0，得到 %v", got)
	}
	if HammingSimilarity(nil, nil) != 0 {
		t.Fatalf("空向量相似度应为 0")
	}
}
