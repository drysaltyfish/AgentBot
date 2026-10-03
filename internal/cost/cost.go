// Package cost 实现 F-66 成本统计与配额：按全局/会话/用户/日/月聚合 LLM
// 调用的 token 与费用，并按配额给出软限告警与硬限决策。
//
// 设计要点：
//   - 聚合在内存中同步完成（廉价、并发安全），持久化经有界队列异步落盘，
//     不阻塞请求路径；
//   - 价格表、配额、时钟、告警回调、指标回调、持久化 Store 全部可注入；
//   - 未知模型的计价策略显式可配，默认按 0 计并告警，绝不静默；
//   - 不持有包级可变状态，所有状态都在 Tracker 实例上。
package cost

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// defaultQueueSize 是异步持久化队列的默认长度。
const defaultQueueSize = 256

// Clock 提供当前时间；生产用 SystemClock，测试注入假时钟以获得确定性。
type Clock interface {
	Now() time.Time
}

// SystemClock 使用真实时间。
type SystemClock struct{}

// Now 返回当前时间。
func (SystemClock) Now() time.Time { return time.Now() }

// Options 是 New 的构造参数。
type Options struct {
	// Prices 是模型价格表；零值表示所有模型都未知。
	Prices PriceTable
	// Quotas 是配额定义，按声明顺序判定。
	Quotas []Quota
	// Store 是可选持久化实现；为 nil 时只做内存统计且不启动后台协程。
	Store Store
	// Clock 为 nil 时使用 SystemClock。
	Clock Clock
	// Warn 是告警回调（未知模型、软/硬限、写失败、队列丢弃）；nil 时静默。
	Warn func(string)
	// Metric 是指标回调，按 (名称, 增量) 上报；nil 时不上报。
	Metric func(string, float64)
	// QueueSize 是异步持久化队列长度；<=0 时取 256。
	QueueSize int
}

// Call 是一次 LLM 调用的计量输入（对应 F-66 规格字段）。
type Call struct {
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	Latency          time.Duration
	SessionKey       string
	UserID           string
	// Timestamp 为零值时取 Clock.Now()。
	Timestamp time.Time
}

// Event 是一次已计价、可持久化的调用记录。
type Event struct {
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	Latency          time.Duration
	SessionKey       string
	UserID           string
	Timestamp        time.Time
}

// Aggregate 是一组调用的聚合用量。
type Aggregate struct {
	Calls            int
	PromptTokens     int
	CompletionTokens int
	Cost             float64
}

// TotalTokens 返回 prompt + completion token 数。
func (a Aggregate) TotalTokens() int { return a.PromptTokens + a.CompletionTokens }

// add 累加一次事件。
func (a *Aggregate) add(ev Event) {
	a.Calls++
	a.PromptTokens += ev.PromptTokens
	a.CompletionTokens += ev.CompletionTokens
	a.Cost += ev.Cost
}

// scopedKey 是聚合写入时的一个 (维度, 键) 组合。
type scopedKey struct {
	scope Scope
	key   string
}

// warnKey 标识某条配额在某个周期窗口内的某个阈值，用于告警去重。
type warnKey struct {
	quota int
	level int
	start string
}

const (
	levelSoft = 1
	levelHard = 2
)

// Tracker 是成本统计与配额的核心；零值不可用，必须经 New 构造。
type Tracker struct {
	mu      sync.Mutex
	prices  PriceTable
	quotas  []Quota
	store   Store
	clock   Clock
	warn    func(string)
	metric  func(string, float64)
	buckets map[bucketKey]Aggregate
	warned  map[warnKey]struct{}
	dropped uint64
	closed  bool

	dirty chan struct{}
	done  chan struct{}
	wg    sync.WaitGroup
}

// New 构造 Tracker。Store 非 nil 时加载快照并启动一个有界异步持久化协程，
// 调用方必须用 Close 关闭它。
func New(opts Options) (*Tracker, error) {
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	t := &Tracker{
		prices:  opts.Prices,
		quotas:  append([]Quota(nil), opts.Quotas...),
		store:   opts.Store,
		clock:   clock,
		warn:    opts.Warn,
		metric:  opts.Metric,
		buckets: make(map[bucketKey]Aggregate),
		warned:  make(map[warnKey]struct{}),
	}
	for i := range t.quotas {
		if t.quotas[i].Action == "" {
			t.quotas[i].Action = ActionDeny
		}
		if err := t.quotas[i].validate(); err != nil {
			return nil, fmt.Errorf("cost: quota[%d]: %w", i, err)
		}
	}
	if t.store != nil {
		snap, err := t.store.Load()
		if err != nil {
			return nil, fmt.Errorf("cost: load: %w", err)
		}
		t.restore(snap)
		size := opts.QueueSize
		if size <= 0 {
			size = defaultQueueSize
		}
		t.dirty = make(chan struct{}, size)
		t.done = make(chan struct{})
		t.wg.Add(1)
		go t.run()
	}
	return t, nil
}

// Record 计量一次调用：按价格表计价、同步更新聚合、触发阈值告警，并把
// 持久化交给有界异步队列（不阻塞请求路径）。返回本次事件与费用。
//
// 未知模型按 UnknownWarnZero 时 cost 为 0 并触发告警；按 UnknownReject 时
// 返回 ErrUnknownModel。
func (t *Tracker) Record(call Call) (Event, error) {
	if call.PromptTokens < 0 || call.CompletionTokens < 0 {
		return Event{}, fmt.Errorf("%w: prompt=%d completion=%d", ErrInvalidUsage, call.PromptTokens, call.CompletionTokens)
	}
	amount, known, err := t.prices.Cost(call.Model, call.PromptTokens, call.CompletionTokens)
	if err != nil {
		return Event{}, err
	}
	ts := call.Timestamp
	if ts.IsZero() {
		ts = t.clock.Now()
	}
	ev := Event{
		Provider:         call.Provider,
		Model:            call.Model,
		PromptTokens:     call.PromptTokens,
		CompletionTokens: call.CompletionTokens,
		Cost:             amount,
		Latency:          call.Latency,
		SessionKey:       call.SessionKey,
		UserID:           call.UserID,
		Timestamp:        ts,
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return Event{}, ErrClosed
	}
	t.applyLocked(ev)
	_, _, warns := t.evaluateLocked(ts, call.SessionKey, call.UserID)
	t.mu.Unlock()

	if !known {
		t.emitWarn(fmt.Sprintf("cost: unknown model %q, recorded as 0", call.Model))
		t.emitMetric("cost_unknown_model_total", 1)
	}
	t.emitWarnings(warns)
	t.emitMetric("cost_usd_total", amount)
	t.emitMetric("cost_tokens_total", float64(call.PromptTokens+call.CompletionTokens))
	t.enqueue()
	return ev, nil
}

// Authorize 在调用前做配额判定。硬限动作是 deny 时返回 *QuotaError 且
// Decision.Allowed 为 false；downgrade 时返回建议模型；其余返回 nil 错误。
func (t *Tracker) Authorize(sessionKey, userID string) (Decision, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return Decision{}, ErrClosed
	}
	dec, qerr, warns := t.evaluateLocked(t.clock.Now(), sessionKey, userID)
	t.mu.Unlock()

	t.emitWarnings(warns)
	if qerr != nil {
		t.emitMetric("cost_quota_exceeded_total", 1)
		return dec, qerr
	}
	return dec, nil
}

// applyLocked 把事件累加到各维度的 total/day/month 桶。
func (t *Tracker) applyLocked(ev Event) {
	day := ev.Timestamp.Format("2006-01-02")
	month := ev.Timestamp.Format("2006-01")
	keys := make([]scopedKey, 0, 3)
	keys = append(keys, scopedKey{scope: ScopeGlobal})
	if ev.SessionKey != "" {
		keys = append(keys, scopedKey{scope: ScopeSession, key: ev.SessionKey})
	}
	if ev.UserID != "" {
		keys = append(keys, scopedKey{scope: ScopeUser, key: ev.UserID})
	}
	for _, k := range keys {
		t.addLocked(k.scope, k.key, PeriodTotal, "", ev)
		t.addLocked(k.scope, k.key, PeriodDay, day, ev)
		t.addLocked(k.scope, k.key, PeriodMonth, month, ev)
	}
}

// addLocked 累加一个聚合桶。
func (t *Tracker) addLocked(scope Scope, key string, period Period, start string, ev Event) {
	bk := bucketKey{scope: scope, key: key, period: period, start: start}
	a := t.buckets[bk]
	a.add(ev)
	t.buckets[bk] = a
}

// usedLocked 返回某条配额在当前窗口已使用的金额。
func (t *Tracker) usedLocked(q Quota, now time.Time, sessionKey, userID string) float64 {
	key := ""
	switch q.Scope {
	case ScopeSession:
		key = sessionKey
	case ScopeUser:
		key = userID
	case ScopeGlobal:
	default:
	}
	return t.buckets[bucketKey{scope: q.Scope, key: key, period: q.Period, start: q.periodStart(now)}].Cost
}

// evaluateLocked 评估全部配额，返回决策、可选拒绝错误与待发告警文本。
// 告警按 (配额, 阈值, 周期窗口) 去重，跨周期自动重置。
func (t *Tracker) evaluateLocked(now time.Time, sessionKey, userID string) (Decision, *QuotaError, []string) {
	dec := Decision{Allowed: true, Action: ActionWarn}
	var qerr *QuotaError
	var warns []string
	for i, q := range t.quotas {
		if q.Scope == ScopeSession && sessionKey == "" {
			continue
		}
		if q.Scope == ScopeUser && userID == "" {
			continue
		}
		used := t.usedLocked(q, now, sessionKey, userID)
		window := q.periodStart(now)
		if soft := q.softLimit(); used >= soft {
			dec.SoftLimit = true
			if t.markLocked(warnKey{quota: i, level: levelSoft, start: window}) {
				warns = append(warns, fmt.Sprintf("cost: quota soft limit %.4f reached for %s/%s (used %.4f, limit %.4f)", soft, q.Scope, q.Period, used, q.Limit))
			}
		}
		if used < q.Limit {
			continue
		}
		if t.markLocked(warnKey{quota: i, level: levelHard, start: window}) {
			warns = append(warns, fmt.Sprintf("cost: quota hard limit %.4f reached for %s/%s (used %.4f)", q.Limit, q.Scope, q.Period, used))
		}
		switch q.Action {
		case ActionDeny:
			dec.Allowed = false
			dec.Action = ActionDeny
			dec.Reason = fmt.Sprintf("%s/%s quota exceeded", q.Scope, q.Period)
			dec.Scope = q.Scope
			dec.Period = q.Period
			dec.Limit = q.Limit
			dec.Used = used
			if qerr == nil {
				qerr = &QuotaError{Scope: q.Scope, Period: q.Period, Limit: q.Limit, Used: used}
			}
		case ActionDowngrade:
			if dec.Allowed {
				dec.Action = ActionDowngrade
				dec.Reason = fmt.Sprintf("%s/%s quota reached, downgrade to %s", q.Scope, q.Period, q.DowngradeModel)
			}
			if dec.DowngradeModel == "" {
				dec.DowngradeModel = q.DowngradeModel
			}
		case ActionWarn:
		default:
		}
	}
	if qerr != nil {
		dec.Allowed = false
		dec.Action = ActionDeny
	}
	return dec, qerr, warns
}

// markLocked 记录一个已发告警；首次返回 true。
func (t *Tracker) markLocked(k warnKey) bool {
	if _, ok := t.warned[k]; ok {
		return false
	}
	t.warned[k] = struct{}{}
	return true
}

// Global 返回全局累计用量。
func (t *Tracker) Global() Aggregate {
	return t.bucket(ScopeGlobal, "", PeriodTotal, "")
}

// Session 返回某会话的累计用量。
func (t *Tracker) Session(key string) Aggregate {
	return t.bucket(ScopeSession, key, PeriodTotal, "")
}

// User 返回某用户的累计用量。
func (t *Tracker) User(id string) Aggregate {
	return t.bucket(ScopeUser, id, PeriodTotal, "")
}

// Today 返回全局当日用量。
func (t *Tracker) Today() Aggregate {
	return t.bucket(ScopeGlobal, "", PeriodDay, t.clock.Now().Format("2006-01-02"))
}

// Month 返回全局当月用量。
func (t *Tracker) Month() Aggregate {
	return t.bucket(ScopeGlobal, "", PeriodMonth, t.clock.Now().Format("2006-01"))
}

// bucket 读取一个聚合桶。
func (t *Tracker) bucket(scope Scope, key string, period Period, start string) Aggregate {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buckets[bucketKey{scope: scope, key: key, period: period, start: start}]
}

// Dropped 返回因持久化队列满而被丢弃的写入次数。
func (t *Tracker) Dropped() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// Snapshot 返回聚合的深拷贝快照，桶按 (scope,key,period,start) 排序。
func (t *Tracker) Snapshot() *Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	buckets := make([]Bucket, 0, len(t.buckets))
	for k, a := range t.buckets {
		buckets = append(buckets, Bucket{Scope: k.scope, Key: k.key, Period: k.period, Start: k.start, Aggregate: a})
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Scope != buckets[j].Scope {
			return buckets[i].Scope < buckets[j].Scope
		}
		if buckets[i].Key != buckets[j].Key {
			return buckets[i].Key < buckets[j].Key
		}
		if buckets[i].Period != buckets[j].Period {
			return buckets[i].Period < buckets[j].Period
		}
		return buckets[i].Start < buckets[j].Start
	})
	return &Snapshot{Buckets: buckets}
}

// restore 把持久化快照载入内存。
func (t *Tracker) restore(snap *Snapshot) {
	if snap == nil {
		return
	}
	for _, b := range snap.Buckets {
		t.buckets[bucketKey{scope: b.Scope, key: b.Key, period: b.Period, start: b.Start}] = b.Aggregate
	}
}

// Flush 同步保存一次快照；Store 为 nil 时无操作。供关闭与测试使用。
func (t *Tracker) Flush() error {
	if t.store == nil {
		return nil
	}
	if err := t.store.Save(t.Snapshot()); err != nil {
		return fmt.Errorf("cost: flush: %w", err)
	}
	return nil
}

// Close 停止后台协程并做最后一次同步保存；可重复调用。
func (t *Tracker) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	hasWorker := t.dirty != nil
	t.mu.Unlock()
	if hasWorker {
		close(t.done)
		t.wg.Wait()
	}
	return t.Flush()
}

// enqueue 非阻塞地把持久化信号放入有界队列；满则计数并告警。
func (t *Tracker) enqueue() {
	if t.dirty == nil {
		return
	}
	select {
	case t.dirty <- struct{}{}:
	default:
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
		t.emitWarn("cost: persistence queue full, save coalesced")
		t.emitMetric("cost_records_dropped_total", 1)
	}
}

// run 是持久化协程：每收到一个信号就保存最新快照（天然合并突发写入）。
func (t *Tracker) run() {
	defer t.wg.Done()
	for {
		select {
		case <-t.done:
			return
		case <-t.dirty:
			if err := t.Flush(); err != nil {
				t.emitWarn(err.Error())
				t.emitMetric("cost_persist_errors_total", 1)
			}
		}
	}
}

// emitWarn 调用告警回调（nil 安全）。
func (t *Tracker) emitWarn(msg string) {
	if t.warn != nil {
		t.warn(msg)
	}
}

// emitMetric 调用指标回调（nil 安全）。
func (t *Tracker) emitMetric(name string, delta float64) {
	if t.metric != nil {
		t.metric(name, delta)
	}
}

// emitWarnings 逐条告警并计数。
func (t *Tracker) emitWarnings(warns []string) {
	for _, w := range warns {
		t.emitWarn(w)
		t.emitMetric("cost_quota_warning_total", 1)
	}
}
