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
				_ = s.Persona()
			}
		}(g)
	}
	wg.Wait()
	if got := m.Len(); got > 50 {
		t.Fatalf("session count exceeded max: actual=%d expected<=50", got)
	}
}

func Test_F21_SessionStateAccessors(t *testing.T) {
	t.Parallel()
	m := New()
	s := m.GetOrCreate(Key{GroupID: 1})
	s.SetPersona("default")
	if s.Persona() != "default" {
		t.Fatalf("Persona: actual=%q", s.Persona())
	}
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
