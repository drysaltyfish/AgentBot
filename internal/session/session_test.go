package session

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/history"
)

func Test_F21_KeyStringIsStableAndIncludesSelfID(t *testing.T) {
	t.Parallel()
	k := Key{SelfID: 1, GroupID: 2, UserID: 3}
	if got := k.String(); got != "1:2:3" {
		t.Fatalf("Key.String: actual=%q expected=%q", got, "1:2:3")
	}
	if !(Key{}).IsZero() {
		t.Fatalf("zero Key should be zero")
	}
	if k.IsZero() {
		t.Fatalf("non-zero Key reported as zero")
	}
}

func Test_F21_MultiAccountSessionsDoNotCollide(t *testing.T) {
	t.Parallel()
	m := New()
	a := m.GetOrCreate(Key{SelfID: 10001, GroupID: 30003})
	b := m.GetOrCreate(Key{SelfID: 10002, GroupID: 30003})
	if a == b {
		t.Fatalf("different selfID produced the same session")
	}
	a.Set("marker", "a")
	if _, ok := b.Get("marker"); ok {
		t.Fatalf("session state leaked across accounts")
	}
	if m.Len() != 2 {
		t.Fatalf("session count: actual=%d expected=2", m.Len())
	}
}

func Test_F21_PolicyShapesTheKey(t *testing.T) {
	t.Parallel()
	const self, group, user = int64(1), int64(2), int64(3)

	perGroup := New(WithPolicy(PerGroup))
	if got := perGroup.KeyFor(self, group, user); got != (Key{SelfID: self, GroupID: group}) {
		t.Fatalf("PerGroup key: actual=%+v", got)
	}
	perUser := New(WithPolicy(PerUser))
	if got := perUser.KeyFor(self, group, user); got != (Key{SelfID: self, UserID: user}) {
		t.Fatalf("PerUser key: actual=%+v", got)
	}
	inGroup := New(WithPolicy(PerUserInGroup))
	if got := inGroup.KeyFor(self, group, user); got != (Key{SelfID: self, GroupID: group, UserID: user}) {
		t.Fatalf("PerUserInGroup key: actual=%+v", got)
	}
}

func Test_F21_MaxEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	m := New(WithMax(2))
	ka := Key{GroupID: 1}
	kb := Key{GroupID: 2}
	kc := Key{GroupID: 3}

	m.GetOrCreate(ka)
	m.GetOrCreate(kb)
	m.GetOrCreate(ka) // ka 变成最近使用
	m.GetOrCreate(kc) // 应该淘汰 kb

	if _, ok := m.Get(kb); ok {
		t.Fatalf("least recently used session was not evicted")
	}
	if _, ok := m.Get(ka); !ok {
		t.Fatalf("recently used session was evicted")
	}
	if _, ok := m.Get(kc); !ok {
		t.Fatalf("new session missing")
	}
	if m.Len() != 2 {
		t.Fatalf("session count: actual=%d expected=2", m.Len())
	}
	if m.Evicted() != 1 {
		t.Fatalf("evicted count: actual=%d expected=1", m.Evicted())
	}
}

func Test_F21_TTLReclaim(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := New(WithTTL(10*time.Minute), WithClock(func() time.Time { return now }))
	m.GetOrCreate(Key{GroupID: 1})
	now = now.Add(11 * time.Minute)
	if n := m.Reclaim(context.Background()); n != 1 {
		t.Fatalf("reclaimed: actual=%d expected=1", n)
	}
	if m.Len() != 0 {
		t.Fatalf("session survived TTL: actual=%d expected=0", m.Len())
	}
}

func Test_F21_ReclaimHookRunsAndRoutesAreDetached(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	var hookCalls atomic.Int64
	var removals atomic.Int64
	m := New(
		WithTTL(time.Minute),
		WithClock(func() time.Time { return now }),
		WithReclaimHook(func(*Session) { hookCalls.Add(1) }),
	)
	s := m.GetOrCreate(Key{GroupID: 1})
	s.AttachRoute(RouteRef{Name: "await-1", Remove: func() { removals.Add(1) }})

	now = now.Add(2 * time.Minute)
	m.Reclaim(context.Background())

	if hookCalls.Load() != 1 {
		t.Fatalf("reclaim hook calls: actual=%d expected=1", hookCalls.Load())
	}
	if removals.Load() != 1 {
		t.Fatalf("attached temp route was not removed: actual=%d expected=1", removals.Load())
	}
}

func Test_F21_CloseClosesAllSessions(t *testing.T) {
	t.Parallel()
	var removals atomic.Int64
	m := New()
	for i := 0; i < 5; i++ {
		s := m.GetOrCreate(Key{GroupID: int64(i)})
		s.AttachRoute(RouteRef{Name: fmt.Sprintf("r%d", i), Remove: func() { removals.Add(1) }})
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: actual=%v expected=nil", err)
	}
	if m.Len() != 0 {
		t.Fatalf("sessions after Close: actual=%d expected=0", m.Len())
	}
	if removals.Load() != 5 {
		t.Fatalf("temp routes removed: actual=%d expected=5", removals.Load())
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func Test_F21_ConcurrentGetOrCreateRespectsMax(t *testing.T) {
	t.Parallel()
	m := New(WithMax(50))
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := Key{GroupID: int64((g*100 + i) % 200)}
				s := m.GetOrCreate(key)
				s.Set("g", g)
			}
		}(g)
	}
	wg.Wait()
	if got := m.Len(); got > 50 {
		t.Fatalf("session count exceeded max: actual=%d expected<=50", got)
	}
}

// Test_F21_SessionStateAccessors 覆盖会话级键值访问。
//
// 这里曾经还测过 Session.Persona/SetPersona，但那套状态没有任何生产调用方——
// 人格真正落在 scoped.Manager（并由 SQLite 持久化）。同一个概念在两个地方安家、
// 其中一个是影子，比少一个方法更难维护，所以删掉了。
func Test_F21_SessionStateAccessors(t *testing.T) {
	t.Parallel()
	m := New()
	s := m.GetOrCreate(Key{GroupID: 1})
	s.Set("k", 1)
	if v, ok := s.Get("k"); !ok || v.(int) != 1 {
		t.Fatalf("Get: actual=(%v,%v)", v, ok)
	}
	s.Delete("k")
	if _, ok := s.Get("k"); ok {
		t.Fatalf("Delete did not remove the key")
	}
	if s.ID != (Key{GroupID: 1}) {
		t.Fatalf("session ID: actual=%+v", s.ID)
	}
}

func Test_F21_HistoryIsSharedWithSessions(t *testing.T) {
	t.Parallel()
	h := history.NewMemory(10)
	m := New(WithHistory(h))
	s := m.GetOrCreate(Key{GroupID: 1})
	if s.Hist != h {
		t.Fatalf("session did not receive the configured history store")
	}
	if err := s.Hist.Append(context.Background(), s.ID.String(), history.Item{Kind: history.KindUser, Content: "hi"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	items, _ := s.Hist.Messages(context.Background(), s.ID.String())
	if len(items) != 1 || items[0].Content != "hi" {
		t.Fatalf("history round trip: %+v", items)
	}
}

// Test_F21_PrivateChatsDoNotShareSession 是串台缺陷的回归测试。
//
// 修复前 PerGroup 在私聊（GroupID=0）时也返回 UserID=0，于是所有私聊共用同一个
// 会话——A 的上下文与记忆会出现在 B 的私聊里。
func Test_F21_PrivateChatsDoNotShareSession(t *testing.T) {
	t.Parallel()
	m := New() // 默认策略即 PerGroup

	keyA := m.KeyFor(1, 0, 100)
	keyB := m.KeyFor(1, 0, 200)
	if keyA == keyB {
		t.Fatalf("不同用户的私聊不得共用会话: %+v == %+v", keyA, keyB)
	}
	if keyA.UserID != 100 || keyB.UserID != 200 {
		t.Fatalf("私聊会话键应包含用户: %+v / %+v", keyA, keyB)
	}

	// 群聊仍按群分桶（同一个群里的不同用户共用会话）。
	g1 := m.KeyFor(1, 555, 100)
	g2 := m.KeyFor(1, 555, 200)
	if g1 != g2 {
		t.Fatalf("同一个群应共用一个会话: %+v != %+v", g1, g2)
	}
	if g1.GroupID != 555 || g1.UserID != 0 {
		t.Fatalf("群会话键应按 F-21 保持 UserID=0: %+v", g1)
	}

	// 私聊与群聊之间也不能混。
	if keyA == g1 {
		t.Fatalf("私聊与群聊不得共用会话")
	}
}
