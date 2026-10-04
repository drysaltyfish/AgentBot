// Package reflect 实现"空闲时反思"的后台任务（F-48 的延伸）：
// 当一段对话**滑出上下文窗口**、且该会话**已经安静下来**时，把那段内容交给模型提炼成
// 长期记忆，避免"聊过但没记住"。
//
// 它花钱，而且是在用户没说话的时候花。因此触发条件是四道闸门**同时**满足：
//
//	静默（距上次活动 > IdleAfter）
//	有新内容（水位之后、且已滑出窗口的条目 > 0）
//	未节流（距上次反思 > MinInterval）
//	有预算（全局每日调用数 < DailyBudget）
//
// 任何一道不满足就完全不调模型——空转的调度不该产生费用。
package reflect

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/scope"
)

// Item 是一条待反思的历史条目（由调用方从存储层取出并渲染好）。
type Item struct {
	// Seq 是条目在会话内的序号，用作反思水位。
	Seq int64
	// SpeakerID 是发言人 QQ 号（私聊为 0）。
	SpeakerID int64
	// SpeakerName 是展示名（群名片优先）。
	SpeakerName string
	// Text 是正文；Ambient 为 true 时表示这不是在跟机器人说话。
	Text    string
	Ambient bool
	// At 是发生时间。
	At time.Time
}

// Source 提供待反思的条目与写入记忆的能力。
//
// 抽成接口是为了让调度器只关心"何时反思"，而"从哪取、写到哪"由组合根决定；
// 也让省钱逻辑可以完全用假实现测试。
type Source interface {
	// Pending 返回某会话中 **seq > since** 且已经滑出窗口的条目（按 seq 升序）。
	Pending(ctx context.Context, sessionKey string, since int64, window int) ([]Item, error)
	// Save 写入一条反思结果：subjectID 为 0 表示公共记忆。
	Save(ctx context.Context, sessionKey string, subjectID int64, text string) error
	// Sessions 返回当前活跃会话的键。
	Sessions() []string
}

// Options 是调度器的配置。
type Options struct {
	// Enabled 为 false 时 Run 立即返回（默认关闭：它会主动花钱）。
	Enabled bool
	// Every 是扫描间隔；<=0 时用 DefaultEvery。
	Every time.Duration
	// IdleAfter 是"这轮聊完了"的判定阈值；<=0 时用 DefaultIdleAfter。
	IdleAfter time.Duration
	// MinInterval 是同一会话两次反思的最小间隔；<=0 时用 DefaultMinInterval。
	MinInterval time.Duration
	// Window 是呈现窗口大小（用于判断"已滑出"）；<=0 时用默认 0（表示由 Source 决定）。
	Window int
	// MaxPerSession 是单会话单次最多写入的事实数；<=0 时用 memory 默认值。
	MaxPerSession int
	// Log 是日志出口。
	Log *observe.Logger
}

// 默认参数。
const (
	// DefaultEvery 是扫描间隔。
	DefaultEvery = time.Minute
	// DefaultIdleAfter 是静默阈值：5 分钟足够判定"这轮聊完了"。
	DefaultIdleAfter = 5 * time.Minute
	// DefaultMinInterval 是同一会话的最小反思间隔。
	DefaultMinInterval = 30 * time.Minute
)

// LastActivity 报告某会话最后一次活动时间。
type LastActivity func(sessionKey string) (time.Time, bool)

// Reflector 对一段文本做反思（由 memory.Reflector 实现）。
type Reflector interface {
	Reflect(ctx context.Context, entries []string) ([]memory.ReflectedFact, error)
}

// Scheduler 是空闲反思的调度器。
type Scheduler struct {
	src      Source
	reflect  Reflector
	activity LastActivity
	opts     Options

	mu     sync.Mutex
	lastAt map[string]time.Time // 会话 -> 上次反思时间
	water  map[string]int64     // 会话 -> 已反思到的 seq
	now    func() time.Time
}

// New 构造调度器。
func New(src Source, r Reflector, activity LastActivity, opts Options) *Scheduler {
	return &Scheduler{
		src: src, reflect: r, activity: activity, opts: opts,
		lastAt: map[string]time.Time{}, water: map[string]int64{},
		now: time.Now,
	}
}

// Watermark 返回某会话当前的水位（已反思到的 seq）。
func (s *Scheduler) Watermark(sessionKey string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.water[sessionKey]
}

// SetWatermark 设置水位（重启用；由组合根从持久化状态恢复）。
func (s *Scheduler) SetWatermark(sessionKey string, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.water[sessionKey] = seq
}

// Run 周期扫描直到 ctx 结束。
func (s *Scheduler) Run(ctx context.Context) {
	if s == nil || !s.opts.Enabled || s.src == nil || s.reflect == nil {
		return
	}
	every := s.opts.Every
	if every <= 0 {
		every = DefaultEvery
	}
	if s.opts.Log != nil {
		s.opts.Log.Component("reflect").Info("idle reflection is enabled",
			"every", every, "idle_after", s.idleAfter(), "min_interval", s.minInterval())
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep 扫描一轮；返回本轮真正发起反思的会话数。
//
// 导出它是为了测试与手动触发：调度逻辑不依赖 ticker。
func (s *Scheduler) Sweep(ctx context.Context) int {
	if s == nil || !s.opts.Enabled || s.src == nil || s.reflect == nil {
		return 0
	}
	now := s.now()
	fired := 0
	for _, key := range s.src.Sessions() {
		if ctx.Err() != nil {
			return fired
		}
		if s.reflectOne(ctx, key, now) {
			fired++
		}
	}
	return fired
}

// reflectOne 处理单个会话；返回是否真的发起了模型调用。
func (s *Scheduler) reflectOne(ctx context.Context, key string, now time.Time) bool {
	// 闸门 1：静默。还在说话就不打扰。
	if s.activity != nil {
		last, ok := s.activity(key)
		if !ok || now.Sub(last) < s.idleAfter() {
			return false
		}
	}
	// 闸门 2：节流。同一会话短时间内不重复反思。
	s.mu.Lock()
	if last, ok := s.lastAt[key]; ok && now.Sub(last) < s.minInterval() {
		s.mu.Unlock()
		return false
	}
	since := s.water[key]
	s.mu.Unlock()

	// 闸门 3：有新内容可反思（水位之后、且已滑出窗口）。
	items, err := s.src.Pending(ctx, key, since, s.opts.Window)
	if err != nil {
		s.warnf("list pending entries failed", "session", key, "error", err)
		return false
	}
	if len(items) == 0 {
		return false
	}

	// 压缩后送进去：这是省钱的关键一步（见 compact）。
	lines, maxSeq := compact(items)
	if len(lines) == 0 {
		s.advance(key, maxSeq, now)
		return false
	}

	facts, err := s.reflect.Reflect(ctx, lines)
	if err != nil {
		// 失败**不推进水位**：下一轮还会重试这批内容。
		s.warnf("reflection failed", "session", key, "error", err)
		return false
	}
	// 闸门 4（预算）在 memory.Reflector 内部判定：额度用尽时返回空且不报错。
	if len(facts) == 0 {
		s.advance(key, maxSeq, now)
		return true
	}

	written := 0
	for _, f := range facts {
		if s.opts.MaxPerSession > 0 && written >= s.opts.MaxPerSession {
			break
		}
		subjectID := int64(0)
		if !f.Shared {
			// 个人事实：归属到本条事实的发言人。
			subjectID = subjectOf(f.Fact, items)
		}
		sctx := scope.WithScope(ctx, key)
		if subjectID > 0 {
			sctx = scope.WithSubject(sctx, subjectID)
		} else {
			sctx = scope.WithoutSubject(sctx)
		}
		if err := s.src.Save(sctx, key, subjectID, f.Fact); err != nil {
			s.warnf("save reflected fact failed", "session", key, "error", err)
			continue
		}
		written++
	}
	s.advance(key, maxSeq, now)
	if written > 0 {
		s.logf("reflected memories written", "session", key, "facts", written, "scanned", len(items))
	}
	return true
}

// advance 推进水位并记录本次反思时间。
func (s *Scheduler) advance(key string, seq int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > s.water[key] {
		s.water[key] = seq
	}
	s.lastAt[key] = now
}

// compact 把条目压成给模型的文本行，并返回最大 seq。
//
// 省钱的三处压缩：
//  1. 连续同人的环境消息合并成一条（"张三 等 3 条"），刷屏不占预算；
//  2. 单条截断到 120 rune——反思要的是"发生了什么"，不是原文；
//  3. 总行数上限 200（配合 Reflector 的 MaxItems 再截）。
//
// 保留发言人与时间：归属判定依赖它们（subjectOf）。
func compact(items []Item) ([]string, int64) {
	const (
		maxRunes  = 120
		maxLines  = 200
		mergeSame = 3
	)
	lines := make([]string, 0, len(items))
	var maxSeq int64
	for i := 0; i < len(items); {
		it := items[i]
		if it.Seq > maxSeq {
			maxSeq = it.Seq
		}
		// 合并连续同人的环境消息。
		j := i
		if it.Ambient {
			for j+1 < len(items) && items[j+1].Ambient && items[j+1].SpeakerID == it.SpeakerID {
				j++
			}
		}
		label := speakerLabel(it)
		switch {
		case j > i && j-i+1 >= mergeSame:
			lines = append(lines, fmt.Sprintf("%s (%s) 连发 %d 条环境消息：%s",
				label, it.At.Format("01-02 15:04"), j-i+1, clip(it.Text, maxRunes)))
		default:
			for k := i; k <= j; k++ {
				lines = append(lines, fmt.Sprintf("%s (%s) %s",
					speakerLabel(items[k]), items[k].At.Format("01-02 15:04"), clip(items[k].Text, maxRunes)))
			}
		}
		if items[j].Seq > maxSeq {
			maxSeq = items[j].Seq
		}
		i = j + 1
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, maxSeq
}

// subjectOf 判断一条事实归属于谁。
//
// 反思输出是自然语言，模型被要求"写明是谁"；这里做一次**保守**的匹配：
// 事实里出现哪个发言人的昵称或 QQ 号，就算谁的；都不匹配时归到最近一位发言人。
// 宁可归错也要有归属——无归属会退化成全群共享，那正是这次要修的问题。
func subjectOf(fact string, items []Item) int64 {
	type cand struct {
		id   int64
		name string
		last int
	}
	seen := map[int64]*cand{}
	order := make([]int64, 0, 4)
	for i, it := range items {
		if it.SpeakerID <= 0 {
			continue
		}
		c, ok := seen[it.SpeakerID]
		if !ok {
			c = &cand{id: it.SpeakerID, name: strings.TrimSpace(it.SpeakerName)}
			seen[it.SpeakerID] = c
			order = append(order, it.SpeakerID)
		}
		c.last = i
	}
	if len(order) == 0 {
		return 0
	}
	// 昵称优先匹配（模型被要求用昵称），再退到 QQ 号。
	for _, id := range order {
		c := seen[id]
		if c.name != "" && strings.Contains(fact, c.name) {
			return id
		}
	}
	for _, id := range order {
		if strings.Contains(fact, fmt.Sprintf("%d", id)) {
			return id
		}
	}
	// 兜底：最近发言的人。
	newest := order[0]
	for _, id := range order {
		if seen[id].last > seen[newest].last {
			newest = id
		}
	}
	return newest
}

func speakerLabel(it Item) string {
	name := strings.TrimSpace(it.SpeakerName)
	switch {
	case name != "" && it.SpeakerID > 0:
		return fmt.Sprintf("%s[QQ%d]", name, it.SpeakerID)
	case it.SpeakerID > 0:
		return fmt.Sprintf("[QQ%d]", it.SpeakerID)
	default:
		return "用户"
	}
}

func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func (s *Scheduler) idleAfter() time.Duration {
	if s.opts.IdleAfter > 0 {
		return s.opts.IdleAfter
	}
	return DefaultIdleAfter
}

func (s *Scheduler) minInterval() time.Duration {
	if s.opts.MinInterval > 0 {
		return s.opts.MinInterval
	}
	return DefaultMinInterval
}

func (s *Scheduler) warnf(msg string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log.Component("reflect").Warn(msg, args...)
	}
}

func (s *Scheduler) logf(msg string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log.Component("reflect").Info(msg, args...)
	}
}
