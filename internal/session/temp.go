package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

var (
	// ErrTempRoutesUnavailable 表示会话管理器没有配置临时路由表。
	ErrTempRoutesUnavailable = errors.New("temp routes are not available")
	// ErrAwaitNoMatch 表示 Await 没有可用的匹配条件（防止误吞全部消息）。
	ErrAwaitNoMatch = errors.New("await requires a match predicate or a non-empty name")
)

// DefaultAwaitTTL 是 Await 的默认超时（配合 ctx 使用）。
const DefaultAwaitTTL = 5 * time.Minute

// TempRoute 是挂在会话上的临时路由（F-15）。
//
// 语义：注册后**优先于常规路由**被匹配；Once 为 true 时触发一次即自动移除；
// TTL 到期后自动失效；会话被回收时一并清理。
type TempRoute struct {
	// Key 是所属会话。
	Key Key
	// Name 便于日志与排查。
	Name string
	// Once 为 true 时触发一次后自动移除。
	Once bool
	// TTL 为 0 表示不过期。
	TTL time.Duration
	// Match 决定事件是否命中；nil 表示该会话的任意事件都命中。
	Match func(*event.Event) bool
	// Deliver 收到命中事件；可为 nil（表示只拦截不投递）。
	Deliver func(*event.Event)
	// Pending 非 nil 时这条等待会被**落盘**（F-86），从而跨重启可见。
	//
	// Match/Deliver 是函数，无法序列化；能持久化的是"存在这样一个等待"这件事。
	// 恢复时由恢复方按 Kind 重新提供行为（见 Restore）。
	Pending *PendingMeta
}

// PendingMeta 是一条等待的持久化描述。
type PendingMeta struct {
	// ID 是稳定标识，由调用方给出（同一次等待重复通知是允许的，按 id 幂等）。
	ID string
	// Kind 决定由谁恢复。
	Kind string
	// Payload 是恢复方解释的内容。
	Payload string
}

// PendingStore 是在途记录的持久化能力。
//
// 定义成本包的最小接口，而不是直接依赖存储包：会话层不该知道 SQL 长什么样。
type PendingStore interface {
	SavePending(ctx context.Context, rec PendingRecord) error
	FinishPending(ctx context.Context, id, status, note string) error
}

// PendingRecord 是落盘时的视图。
type PendingRecord struct {
	ID         string
	SessionKey string
	Kind       string
	Payload    string
	CreatedAt  int64
	ExpiresAt  int64
}

type tempEntry struct {
	route   TempRoute
	expires time.Time
}

// TempTable 管理会话级临时路由。
//
// 并发安全：注册/投递/移除可能来自读循环、工作池与会话回收三条路径。
type TempTable struct {
	mu    sync.Mutex
	byKey map[Key][]*tempEntry
	now   func() time.Time

	pending PendingStore
	warn    func(string)
}

// NewTempTable 构造临时路由表。
func NewTempTable() *TempTable {
	return &TempTable{byKey: map[Key][]*tempEntry{}, now: time.Now}
}

// WithClock 注入时间源（测试用）。
func (t *TempTable) WithClock(now func() time.Time) *TempTable {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now != nil {
		t.now = now
	}
	return t
}

// WithPendingStore 挂上在途记录的持久化能力（F-86）。
func (t *TempTable) WithPendingStore(ps PendingStore, warn func(string)) *TempTable {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = ps
	t.warn = warn
	return t
}

// Register 注册一条临时路由，返回幂等的移除函数。
//
// 带 Pending 的路由会被落盘；移除时状态置为 done。**落盘失败不影响注册**——
// 等待本身仍然有效，只是重启后看不见了，因此只告警。
func (t *TempTable) Register(r TempRoute) func() {
	t.mu.Lock()
	e := &tempEntry{route: r}
	if r.TTL > 0 {
		e.expires = t.now().Add(r.TTL)
	}
	t.byKey[r.Key] = append(t.byKey[r.Key], e)
	ps, warn := t.pending, t.warn
	t.mu.Unlock()

	if r.Pending != nil && ps != nil {
		var expiresAt int64
		if !e.expires.IsZero() {
			expiresAt = e.expires.UnixMilli()
		}
		// 刻意用 Background：临时路由的生命周期长于任何一次请求，
		// 清理不能因为触发它的请求被取消而中断。
		//nolint:contextcheck // 见上：后台收尾语义
		err := ps.SavePending(context.Background(), PendingRecord{
			ID: r.Pending.ID, SessionKey: r.Key.String(), Kind: r.Pending.Kind,
			Payload: r.Pending.Payload, CreatedAt: t.now().UnixMilli(), ExpiresAt: expiresAt,
		})
		if err != nil && warn != nil {
			warn("cannot persist pending operation; it will not survive a restart: " + err.Error())
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			t.removeEntry(r.Key, e)
			if r.Pending != nil && ps != nil {
				if err := ps.FinishPending(context.Background(), r.Pending.ID, "done", "等待完成"); err != nil && warn != nil { //nolint:contextcheck // 后台收尾语义
					warn("cannot close pending operation: " + err.Error())
				}
			}
		})
	}
}

// FinishPending 把一条在途记录标记为结束（供恢复与超时路径使用）。
func (t *TempTable) FinishPending(ctx context.Context, id, status, note string) error {
	t.mu.Lock()
	ps := t.pending
	t.mu.Unlock()
	if ps == nil {
		return nil
	}
	return ps.FinishPending(ctx, id, status, note)
}

// Offer 把事件交给该会话的临时路由。
//
// 命中则消费并返回 true——调用方据此**不再走常规路由**（F-15 的优先级要求）。
// 过期的条目会在匹配过程中被顺手清理。
func (t *TempTable) Offer(key Key, ev *event.Event) bool {
	if ev == nil {
		return false
	}
	t.mu.Lock()
	entries := t.byKey[key]
	now := t.now()

	// 先清理过期项，保持有界；过期的在途记录要收尾，否则重启后会以为还在等。
	live := entries[:0]
	var expired []*tempEntry
	for _, e := range entries {
		if e.expired(now) {
			expired = append(expired, e)
			continue
		}
		live = append(live, e)
	}
	t.byKey[key] = live
	ps := t.pending
	t.mu.Unlock()
	if ps != nil {
		for _, e := range expired {
			if e.route.Pending == nil {
				continue
			}
			_ = ps.FinishPending(context.Background(), e.route.Pending.ID, "expired", "等待超时") //nolint:contextcheck // 后台收尾语义
		}
	}
	t.mu.Lock()

	hit := -1
	for i, e := range live {
		if e.route.Match == nil || e.route.Match(ev) {
			hit = i
			break
		}
	}
	if hit < 0 {
		t.mu.Unlock()
		return false
	}

	chosen := live[hit]
	if chosen.route.Once {
		t.byKey[key] = append(live[:hit], live[hit+1:]...)
	}
	t.mu.Unlock()

	if chosen.route.Deliver != nil {
		chosen.route.Deliver(ev)
	}
	return true
}

// RemoveKey 移除某会话的全部临时路由，返回移除数量（会话回收时调用）。
func (t *TempTable) RemoveKey(key Key) int {
	t.mu.Lock()
	entries := t.byKey[key]
	delete(t.byKey, key)
	n := len(entries)
	ps, warn := t.pending, t.warn
	t.mu.Unlock()

	// 会话被回收：在途记录一并收尾，否则重启后会被当成"仍在等待"。
	if ps != nil {
		for _, e := range entries {
			if e.route.Pending == nil {
				continue
			}
			if err := ps.FinishPending(context.Background(), e.route.Pending.ID, "orphaned",
				"会话已回收"); err != nil && warn != nil { //nolint:contextcheck // 后台收尾语义
				warn("cannot close pending operation: " + err.Error())
			}
		}
	}
	return n
}

// Len 返回当前临时路由总数。
func (t *TempTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for _, entries := range t.byKey {
		total += len(entries)
	}
	return total
}

func (e *tempEntry) expired(now time.Time) bool {
	return !e.expires.IsZero() && now.After(e.expires)
}

func (t *TempTable) removeEntry(key Key, target *tempEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := t.byKey[key]
	for i, e := range entries {
		if e == target {
			t.byKey[key] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(t.byKey[key]) == 0 {
		delete(t.byKey, key)
	}
}
