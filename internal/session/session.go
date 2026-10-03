// Package session 实现会话归属、粒度策略与回收（FEATURES.md F-21）。
//
// 会话内的可变状态一律经方法访问（内部加锁），不允许外部直接改字段。
package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// Key 是会话键。
//
// 必须包含 SelfID：否则同一群在多账号接入时会共用会话与记忆（F-07）。
type Key struct {
	SelfID  int64
	GroupID int64
	UserID  int64
}

// String 返回稳定的字符串形式；日志与指标里的 session_key 一律用它。
func (k Key) String() string {
	return fmt.Sprintf("%d:%d:%d", k.SelfID, k.GroupID, k.UserID)
}

// IsZero 判断是否为零值。
func (k Key) IsZero() bool { return k == Key{} }

// Policy 是会话粒度策略。
type Policy string

// 策略常量。
const (
	PerGroup       Policy = "per-group"
	PerUser        Policy = "per-user"
	PerUserInGroup Policy = "per-user-in-group"
)

// 默认参数。
const (
	DefaultTTL          = 30 * time.Minute
	DefaultMax          = 10000
	DefaultReclaimEvery = time.Minute
)

// RouteRef 是挂在会话上的临时路由引用（F-15/F-16 在 M2 填入具体实现）。
type RouteRef struct {
	Name   string
	Remove func()
}

// Session 是一次会话的状态容器。
type Session struct {
	ID     Key
	Hist   history.History
	caller transport.Caller

	mu       sync.Mutex
	persona  string
	lastSeen time.Time
	data     map[string]any
	routes   []RouteRef
}

func newSession(key Key, hist history.History, caller transport.Caller, now time.Time) *Session {
	return &Session{ID: key, Hist: hist, caller: caller, lastSeen: now, data: map[string]any{}}
}

// Touch 刷新活跃时间。
func (s *Session) Touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen = now
}

// LastSeen 返回最后活跃时间。
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// Persona 返回当前人格名（F-82 的作用域键）。
func (s *Session) Persona() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persona
}

// SetPersona 切换人格；切换人格不需要重建会话。
func (s *Session) SetPersona(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persona = name
}

// Set 写入会话级数据。
func (s *Session) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]any{}
	}
	s.data[key] = value
}

// Get 读取会话级数据。
func (s *Session) Get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return v, ok
}

// Delete 删除会话级数据。
func (s *Session) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Caller 返回该会话绑定到哪个账号的调用器。
func (s *Session) Caller() transport.Caller {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caller
}

// SetCaller 更新绑定的调用器（多账号切换时用）。
func (s *Session) SetCaller(c transport.Caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caller = c
}

// AttachRoute 登记一条挂在会话上的临时路由，回收时会一并注销。
func (s *Session) AttachRoute(ref RouteRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, ref)
}

// DetachRoute 注销登记（幂等）。
func (s *Session) DetachRoute(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.routes[:0]
	for _, r := range s.routes {
		if r.Name != name {
			kept = append(kept, r)
		}
	}
	s.routes = kept
}

// RouteRefs 返回已登记的临时路由副本。
func (s *Session) RouteRefs() []RouteRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RouteRef, len(s.routes))
	copy(out, s.routes)
	return out
}

// closeRoutes 注销该会话名下的全部临时路由。
func (s *Session) closeRoutes() {
	s.mu.Lock()
	refs := s.routes
	s.routes = nil
	s.mu.Unlock()
	for _, ref := range refs {
		if ref.Remove != nil {
			ref.Remove()
		}
	}
}

// Option 配置 Manager。
type Option func(*Manager)

// WithPolicy 设置会话粒度策略。
func WithPolicy(p Policy) Option { return func(m *Manager) { m.policy = p } }

// WithTTL 设置空闲回收阈值。
func WithTTL(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.ttl = d
		}
	}
}

// WithMax 设置会话数上限。
func WithMax(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.max = n
		}
	}
}

// WithHistory 设置历史存储实现。
func WithHistory(h history.History) Option {
	return func(m *Manager) {
		if h != nil {
			m.hist = h
		}
	}
}

// WithClock 注入时间源。
func WithClock(now func() time.Time) Option {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// WithReclaimHook 设置回收前的固化回调（例如落盘书签记忆）。
func WithReclaimHook(fn func(*Session)) Option {
	return func(m *Manager) { m.onReclaim = fn }
}

// Manager 持有全部会话并按 LRU + TTL 回收。
type Manager struct {
	mu        sync.Mutex
	sessions  map[Key]*Session
	order     []Key
	policy    Policy
	ttl       time.Duration
	max       int
	hist      history.History
	now       func() time.Time
	onReclaim func(*Session)
	evicted   int
	temp      *TempTable
}

// New 构造 Manager。
func New(opts ...Option) *Manager {
	m := &Manager{
		sessions: map[Key]*Session{},
		policy:   PerGroup,
		ttl:      DefaultTTL,
		max:      DefaultMax,
		now:      time.Now,
		temp:     NewTempTable(),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Name 实现 bot.Component。
func (m *Manager) Name() string { return "session-manager" }

// Temp 返回临时路由表（F-15）；读循环应先用它 Offer，命中则不再走常规路由。
func (m *Manager) Temp() *TempTable {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.temp == nil {
		m.temp = NewTempTable()
	}
	return m.temp
}

// Await 等待该会话的下一条消息（F-16）。
//
// 实现方式是一条 Once 的临时路由 + 一个带缓冲的 channel：它**不阻塞事件读循环**，
// 只是把消息投递进来。ctx 取消或 TTL 到期都会返回错误，并且不留下悬挂的临时路由。
func (m *Manager) Await(ctx context.Context, key Key, match func(*event.Event) bool) (*event.Event, error) {
	if match == nil {
		return nil, ErrAwaitNoMatch
	}
	temp := m.Temp()
	if temp == nil {
		return nil, ErrTempRoutesUnavailable
	}

	ch := make(chan *event.Event, 1)
	remove := temp.Register(TempRoute{
		Key:   key,
		Name:  "await",
		Once:  true,
		TTL:   DefaultAwaitTTL,
		Match: match,
		Deliver: func(ev *event.Event) {
			// 带缓冲 + 非阻塞投递：读循环绝不因为等待方来不及取而被拖住。
			select {
			case ch <- ev:
			default:
			}
		},
	})
	defer remove()

	select {
	case ev := <-ch:
		return ev, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// KeyFor 按策略把事件字段折成会话键。
func (m *Manager) KeyFor(selfID, groupID, userID int64) Key {
	switch m.policy {
	case PerGroup:
		return Key{SelfID: selfID, GroupID: groupID}
	case PerUser:
		return Key{SelfID: selfID, UserID: userID}
	case PerUserInGroup:
		return Key{SelfID: selfID, GroupID: groupID, UserID: userID}
	default:
		return Key{SelfID: selfID, GroupID: groupID}
	}
}

// GetOrCreate 取回或创建会话；超过 max 时按 LRU 淘汰最久未使用的一个。
func (m *Manager) GetOrCreate(key Key) *Session {
	m.mu.Lock()
	now := m.now()
	if s, ok := m.sessions[key]; ok {
		s.Touch(now)
		m.touchOrder(key)
		m.mu.Unlock()
		return s
	}
	s := newSession(key, m.hist, nil, now)
	m.sessions[key] = s
	m.order = append(m.order, key)

	// 先收集待淘汰的会话，释放锁后再固化，避免在持锁时执行用户代码。
	var victims []*Session
	for len(m.sessions) > m.max {
		oldest := m.order[0]
		m.order = m.order[1:]
		if victim, ok := m.sessions[oldest]; ok {
			delete(m.sessions, oldest)
			m.evicted++
			victims = append(victims, victim)
		}
	}
	m.mu.Unlock()

	for _, v := range victims {
		m.finalize(v)
	}
	return s
}

func (m *Manager) touchOrder(key Key) {
	for i, k := range m.order {
		if k == key {
			m.order = append(append(m.order[:i], m.order[i+1:]...), key)
			return
		}
	}
}

// Get 取回已存在的会话。
func (m *Manager) Get(key Key) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	return s, ok
}

// Len 返回当前会话数。
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Evicted 返回因超上限而淘汰的会话数。
func (m *Manager) Evicted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evicted
}

// Reclaim 回收超时未活跃的会话，返回回收数量。
//
// 先把待回收列表摘出来、释放锁，再逐个 finalize（不在持锁时执行用户代码）。
func (m *Manager) Reclaim(ctx context.Context) int {
	now := m.now()
	m.mu.Lock()
	var victims []*Session
	for key, s := range m.sessions {
		if now.Sub(s.LastSeen()) > m.ttl {
			victims = append(victims, s)
			delete(m.sessions, key)
			m.removeOrder(key)
		}
	}
	m.mu.Unlock()

	for _, s := range victims {
		if ctx.Err() != nil {
			break
		}
		m.finalize(s)
	}
	return len(victims)
}

func (m *Manager) removeOrder(key Key) {
	kept := m.order[:0]
	for _, k := range m.order {
		if k != key {
			kept = append(kept, k)
		}
	}
	m.order = kept
}

func (m *Manager) finalize(s *Session) {
	s.closeRoutes()
	// F-15：会话回收时一并清理其临时路由，避免悬挂的 Await 把后续消息吞掉。
	if m.temp != nil {
		m.temp.RemoveKey(s.ID)
	}
	if m.onReclaim != nil {
		m.onReclaim(s)
	}
}

// StartReclaimer 起一个后台 ticker；ctx 取消时退出。
func (m *Manager) StartReclaimer(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = DefaultReclaimEvery
	}
	ticker := time.NewTicker(every)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Reclaim(ctx)
			}
		}
	}()
}

// Close 关闭全部会话（供 Bot.Shutdown 的 PhaseSession 调用）。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	victims := make([]*Session, 0, len(m.sessions))
	for key, s := range m.sessions {
		victims = append(victims, s)
		delete(m.sessions, key)
	}
	m.order = nil
	m.mu.Unlock()

	for _, s := range victims {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.finalize(s)
	}
	return nil
}
