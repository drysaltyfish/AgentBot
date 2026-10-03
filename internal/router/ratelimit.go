package router

import (
	"sync"
	"time"
)

// Limiter 是单 key 的令牌桶（F-18）。
//
// 语义：桶容量 burst，按 rate（每秒）补充；`AllowN(n)` 先按经过时间补币再扣，
// 不足则拒绝。时间回拨时**不补币也不扣成负数**——否则一次 NTP 校正就能把额度放大。
type Limiter struct {
	mu        sync.Mutex
	tokens    float64
	last      time.Time
	rate      float64
	burst     float64
	unlimited bool
	now       func() time.Time
}

// NewLimiter 构造令牌桶；rate 或 burst <= 0 视为**不限制**（配置校验负责拦住误配）。
//
// 初值给满桶：冷启动的第一批请求不该先等一个补充周期。
func NewLimiter(rate, burst float64) *Limiter {
	l := &Limiter{rate: rate, burst: burst, now: time.Now}
	if rate <= 0 || burst <= 0 {
		l.unlimited = true
		return l
	}
	l.tokens = burst
	return l
}

// WithClock 注入时间源（测试用），与 history.Memory 的约定一致。
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	if now != nil {
		l.now = now
	}
	return l
}

// Allow 是 AllowN(1) 的简写。
func (l *Limiter) Allow() bool { return l.AllowN(1) }

// AllowN 尝试扣减 n 个令牌。
func (l *Limiter) AllowN(n float64) bool {
	if l == nil || l.unlimited || n <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	switch {
	case l.last.IsZero():
		l.last = now
	case now.After(l.last):
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	default:
		// 时间回拨：不补币、不动基准点。
	}

	if l.tokens < n {
		return false
	}
	l.tokens -= n
	return true
}

// limiterEntry 是 manager 里的一条记录；last 是"最后一次使用"，
// 用于 TTL 回收（Limiter 自己的 last 是补币基准，两者语义不同）。
type limiterEntry struct {
	lim  *Limiter
	last time.Time
}

// LimiterManager 按 key 惰性创建令牌桶，并按 TTL 回收空闲条目（F-18）。
//
// 无界增长是这类实现最常见的泄漏：key 来自用户/群，不回收迟早吃满内存。
// 因此这里在 Allow 里按 TTL 顺带清扫（不额外起 goroutine，确定性可测）。
type LimiterManager[K comparable] struct {
	mu        sync.Mutex
	entries   map[K]*limiterEntry
	rate      float64
	burst     float64
	ttl       time.Duration
	nextSweep time.Time
	now       func() time.Time
}

// NewLimiterManager 构造管理器；TTL 取 burst/rate*3（F-18 规格）。
func NewLimiterManager[K comparable](rate, burst float64) *LimiterManager[K] {
	m := &LimiterManager[K]{entries: map[K]*limiterEntry{}, rate: rate, burst: burst, now: time.Now}
	if rate > 0 && burst > 0 {
		m.ttl = time.Duration(burst / rate * 3 * float64(time.Second))
		if m.ttl <= 0 {
			m.ttl = time.Minute
		}
	}
	return m
}

// WithClock 注入时间源（测试用）。
func (m *LimiterManager[K]) WithClock(now func() time.Time) *LimiterManager[K] {
	if now != nil {
		m.now = now
	}
	return m
}

// Allow 按 key 扣减 n 个令牌；key 首次出现时惰性建桶。
func (m *LimiterManager[K]) Allow(key K, n float64) bool {
	now := m.now()
	m.mu.Lock()
	m.maybeSweepLocked(now)
	e, ok := m.entries[key]
	if !ok {
		e = &limiterEntry{lim: NewLimiter(m.rate, m.burst).WithClock(m.now)}
		m.entries[key] = e
	}
	e.last = now
	lim := e.lim
	m.mu.Unlock()

	return lim.AllowN(n)
}

// Len 返回当前活跃 key 数（观测/测试用）。
func (m *LimiterManager[K]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Sweep 立即回收空闲条目并返回回收数量。
func (m *LimiterManager[K]) Sweep() int {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	before := len(m.entries)
	m.sweepLocked(now)
	return before - len(m.entries)
}

// maybeSweepLocked 最多每 TTL 清扫一次，避免热路径每次都遍历全表。
func (m *LimiterManager[K]) maybeSweepLocked(now time.Time) {
	if m.ttl <= 0 {
		return
	}
	if !m.nextSweep.IsZero() && now.Before(m.nextSweep) {
		return
	}
	m.nextSweep = now.Add(m.ttl)
	m.sweepLocked(now)
}

func (m *LimiterManager[K]) sweepLocked(now time.Time) {
	for k, e := range m.entries {
		if now.Sub(e.last) >= m.ttl {
			delete(m.entries, k)
		}
	}
}

// Rule 把限速挂成 mid 钩子：命中即按 keyFn 限速，超限返回 false 让引擎拒绝本轮。
//
// 用法：engine.UseMid(limiter.Rule(func(c *Ctx) int64 { return c.Event.UserID }, nil))
//
// 注意 mid 钩子对**每条路由**都生效，因此超限事件会被整条丢弃（连"只记录"的
// 兜底路由也不执行）。这是刻意的：限速的目的就是让刷屏不产生任何 LLM 调用，
// 而记录一条环境消息的价值远低于额度被打爆的代价。
func (m *LimiterManager[K]) Rule(keyFn func(*Ctx) K, onLimit func(*Ctx)) Rule {
	return func(c *Ctx) bool {
		if c == nil || c.Event == nil {
			return true
		}
		if m.Allow(keyFn(c), 1) {
			return true
		}
		if onLimit != nil {
			onLimit(c)
		}
		return false
	}
}
