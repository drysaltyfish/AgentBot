package moderation

import (
	"fmt"
	"sync"
	"time"
)

// 防刷默认值（F-58 规格）：10 秒 20 条 → 封 60 秒。
const (
	DefaultSpamWindow          = 10 * time.Second
	DefaultSpamMaxMessages     = 20
	DefaultSpamBanDuration     = 60 * time.Second
	DefaultSpamDuplicateRepeat = 5
	DefaultSpamDuplicateWindow = 10 * time.Second
	maxTrackedSpamKeys         = 100000
)

// SpamReason 是防刷命中的种类。
type SpamReason uint8

// 防刷命中种类；零值表示未命中。
const (
	// SpamNone 表示未超限。
	SpamNone SpamReason = iota
	// SpamRate 表示短时间消息量超限。
	SpamRate
	// SpamDuplicate 表示连续重复相同消息。
	SpamDuplicate
)

// String 返回稳定的字符串形式。
func (r SpamReason) String() string {
	switch r {
	case SpamNone:
		return "none"
	case SpamRate:
		return "rate"
	case SpamDuplicate:
		return "duplicate"
	default:
		return "unknown"
	}
}

// AntiSpamConfig 是防刷参数；零值经归一化后等价于规格默认值。
type AntiSpamConfig struct {
	// Window 是速率统计窗口，<=0 时取 DefaultSpamWindow。
	Window time.Duration
	// MaxMessages 是窗口内允许的最大消息数，<=0 时取 DefaultSpamMaxMessages。
	MaxMessages int
	// BanDuration 是触发后的临时封禁时长，<=0 时取 DefaultSpamBanDuration。
	BanDuration time.Duration
	// DuplicateRepeat 是连续相同消息的阈值，<=0 时取 DefaultSpamDuplicateRepeat。
	DuplicateRepeat int
	// DuplicateWindow 是重复消息的统计窗口，<=0 时取 Window。
	DuplicateWindow time.Duration
}

// normalized 回填零值字段。
func (c AntiSpamConfig) normalized() AntiSpamConfig {
	if c.Window <= 0 {
		c.Window = DefaultSpamWindow
	}
	if c.MaxMessages <= 0 {
		c.MaxMessages = DefaultSpamMaxMessages
	}
	if c.BanDuration <= 0 {
		c.BanDuration = DefaultSpamBanDuration
	}
	if c.DuplicateRepeat <= 0 {
		c.DuplicateRepeat = DefaultSpamDuplicateRepeat
	}
	if c.DuplicateWindow <= 0 {
		c.DuplicateWindow = c.Window
	}
	return c
}

// spamBucket 是单个 key 的滑动窗口状态。
type spamBucket struct {
	times    []time.Time
	lastText string
	repeats  int
	lastSeen time.Time
}

// AntiSpam 按用户/群维度做速率与重复消息检测；并发安全。
//
// 不额外起 goroutine：过期桶在 Record / Sweep 时顺带回收，确定性可测。
type AntiSpam struct {
	cfg AntiSpamConfig

	mu      sync.Mutex
	buckets map[string]*spamBucket
	now     func() time.Time
}

// NewAntiSpam 构造防刷器。
func NewAntiSpam(cfg AntiSpamConfig) *AntiSpam {
	return &AntiSpam{cfg: cfg.normalized(), buckets: make(map[string]*spamBucket), now: time.Now}
}

// WithClock 注入时间源（测试用）；nil 时保持 time.Now。
func (a *AntiSpam) WithClock(now func() time.Time) *AntiSpam {
	if a != nil && now != nil {
		a.now = now
	}
	return a
}

// Config 返回归一化后的配置。
func (a *AntiSpam) Config() AntiSpamConfig {
	if a == nil {
		return AntiSpamConfig{}.normalized()
	}
	return a.cfg
}

// BanDuration 返回触发后应临时封禁的时长。
func (a *AntiSpam) BanDuration() time.Duration {
	if a == nil {
		return 0
	}
	return a.cfg.BanDuration
}

// Record 记录一条来自 key 的消息，返回命中种类与明确原因。
//
// key 由调用方编码为 “用户 ID” 或 “群 ID” 的字符串形式；text 用于重复检测。
func (a *AntiSpam) Record(key, text string) (SpamReason, string) {
	if a == nil {
		return SpamNone, ""
	}
	return a.recordAt(key, text, a.now())
}

// RecordAt 是 Record 的可注入时间版本，便于确定性测试。
func (a *AntiSpam) RecordAt(key, text string, now time.Time) (SpamReason, string) {
	if a == nil {
		return SpamNone, ""
	}
	return a.recordAt(key, text, now)
}

func (a *AntiSpam) recordAt(key, text string, now time.Time) (SpamReason, string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	b, ok := a.buckets[key]
	if !ok {
		if len(a.buckets) >= maxTrackedSpamKeys {
			a.sweepLocked(now)
		}
		b = &spamBucket{}
		a.buckets[key] = b
	}
	prevSeen := b.lastSeen
	b.lastSeen = now

	// 速率：滑动窗口内保留最近的到达时间。
	kept := b.times[:0]
	for _, ts := range b.times {
		if now.Sub(ts) < a.cfg.Window {
			kept = append(kept, ts)
		}
	}
	b.times = append(kept, now)

	// 重复：连续相同文本计数；窗口外重置。
	if text != "" && text == b.lastText && !prevSeen.IsZero() && now.Sub(prevSeen) <= a.cfg.DuplicateWindow {
		b.repeats++
	} else {
		b.lastText = text
		b.repeats = 1
	}

	if len(b.times) > a.cfg.MaxMessages {
		return SpamRate, fmt.Sprintf("短时间消息过多：%s 内 %d 条（上限 %d 条）",
			a.cfg.Window, len(b.times), a.cfg.MaxMessages)
	}
	if text != "" && b.repeats >= a.cfg.DuplicateRepeat {
		return SpamDuplicate, fmt.Sprintf("连续重复相同消息 %d 次（上限 %d 次）", b.repeats, a.cfg.DuplicateRepeat)
	}
	return SpamNone, ""
}

// Len 返回当前跟踪的 key 数量（观测/测试用）。
func (a *AntiSpam) Len() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.buckets)
}

// Sweep 回收空闲桶并返回回收数量。
func (a *AntiSpam) Sweep() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sweepLocked(a.now())
}

func (a *AntiSpam) sweepLocked(now time.Time) int {
	idle := a.cfg.Window
	if a.cfg.DuplicateWindow > idle {
		idle = a.cfg.DuplicateWindow
	}
	removed := 0
	for k, b := range a.buckets {
		if now.Sub(b.lastSeen) >= idle {
			delete(a.buckets, k)
			removed++
		}
	}
	return removed
}
