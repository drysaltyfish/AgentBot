package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

func ev(userID int64) *event.Event {
	return &event.Event{Kind: event.KindMessage, SelfID: 1, UserID: userID}
}

// Test_F15_TempRouteTakesPriorityAndIsOnce 覆盖 F-15 的核心语义。
func Test_F15_TempRouteTakesPriorityAndIsOnce(t *testing.T) {
	t.Parallel()
	tt := NewTempTable()
	key := Key{SelfID: 1, UserID: 100}

	var got []*event.Event
	remove := tt.Register(TempRoute{
		Key:   key,
		Name:  "capture",
		Once:  true,
		Match: func(e *event.Event) bool { return e.UserID == 100 },
		Deliver: func(e *event.Event) {
			got = append(got, e)
		},
	})
	defer remove()

	// 命中：被消费，调用方据此不再走常规路由。
	if !tt.Offer(key, ev(100)) {
		t.Fatalf("临时路由应命中")
	}
	// once：第二次不再命中。
	if tt.Offer(key, ev(100)) {
		t.Fatalf("once 路由不应再次命中")
	}
	if len(got) != 1 {
		t.Fatalf("投递次数: %d", len(got))
	}
	if tt.Len() != 0 {
		t.Fatalf("once 触发后应自动移除: %d", tt.Len())
	}
}

func Test_F15_NonMatchingEventsAreNotConsumed(t *testing.T) {
	t.Parallel()
	tt := NewTempTable()
	key := Key{SelfID: 1, UserID: 100}
	remove := tt.Register(TempRoute{
		Key:   key,
		Name:  "only-100",
		Match: func(e *event.Event) bool { return e.UserID == 100 },
	})
	defer remove()

	if tt.Offer(key, ev(200)) {
		t.Fatalf("不匹配的事件不应被消费")
	}
	if !tt.Offer(key, ev(100)) {
		t.Fatalf("匹配的事件应被消费")
	}
}

func Test_F15_TTLExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tt := NewTempTable().WithClock(func() time.Time { return now })
	key := Key{SelfID: 1, UserID: 100}

	tt.Register(TempRoute{Key: key, Name: "ttl", TTL: time.Second})
	if !tt.Offer(key, ev(100)) {
		t.Fatalf("未过期应命中")
	}

	now = now.Add(2 * time.Second)
	if tt.Offer(key, ev(100)) {
		t.Fatalf("已过期不应命中")
	}
	if tt.Len() != 0 {
		t.Fatalf("过期项应在匹配过程中被清理: %d", tt.Len())
	}
}

// Test_F15_SessionCleanupRemovesTempRoutes 覆盖"会话回收时自动清理"。
func Test_F15_SessionCleanupRemovesTempRoutes(t *testing.T) {
	t.Parallel()
	m := New()
	key := Key{SelfID: 1, UserID: 100}
	s := m.GetOrCreate(key)

	var delivered int
	m.Temp().Register(TempRoute{Key: key, Name: "pending", Deliver: func(*event.Event) { delivered++ }})
	if m.Temp().Len() != 1 {
		t.Fatalf("注册后应有 1 条: %d", m.Temp().Len())
	}

	m.finalize(s)
	if m.Temp().Len() != 0 {
		t.Fatalf("会话回收后临时路由应被清理: %d", m.Temp().Len())
	}
	if m.Temp().Offer(key, ev(100)) {
		t.Fatalf("清理后不应再吞消息（否则 Await 悬挂会吃掉后续消息）")
	}
	_ = delivered
}

// Test_F16_AwaitReceivesNextMessage 是 F-16 的验收点。
func Test_F16_AwaitReceivesNextMessage(t *testing.T) {
	t.Parallel()
	m := New()
	key := Key{SelfID: 1, UserID: 100}

	type result struct {
		ev  *event.Event
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		e, err := m.Await(ctx, key, func(e *event.Event) bool { return e.UserID == 100 })
		done <- result{e, err}
	}()

	// 等 Await 注册好临时路由再投递。
	waitForTemp(t, m, 1)
	if !m.Temp().Offer(key, ev(100)) {
		t.Fatalf("Offer 应命中并唤醒 Await")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Await 失败: %v", r.err)
		}
		if r.ev == nil || r.ev.UserID != 100 {
			t.Fatalf("收到的事件不对: %+v", r.ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Await 未被唤醒")
	}
}

// Test_F16_AwaitDoesNotLeakTempRoutes 覆盖取消后不留悬挂路由。
func Test_F16_AwaitDoesNotLeakTempRoutes(t *testing.T) {
	t.Parallel()
	m := New()
	key := Key{SelfID: 1, UserID: 100}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := m.Await(ctx, key, func(e *event.Event) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled: %v", err)
	}
	if m.Temp().Len() != 0 {
		t.Fatalf("取消后不应留下悬挂的临时路由: %d", m.Temp().Len())
	}
	// 关键：后续消息不能被一个已经离开的等待者吞掉。
	if m.Temp().Offer(key, ev(100)) {
		t.Fatalf("取消后不应再消费消息")
	}
}

// Test_F16_AwaitIsSessionScoped 覆盖多会话互不串扰。
func Test_F16_AwaitIsSessionScoped(t *testing.T) {
	t.Parallel()
	m := New()
	keyA := Key{SelfID: 1, UserID: 100}
	keyB := Key{SelfID: 1, UserID: 200}

	type result struct {
		key Key
		ev  *event.Event
	}
	done := make(chan result, 2)
	for _, k := range []Key{keyA, keyB} {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			e, err := m.Await(ctx, k, func(e *event.Event) bool { return true })
			if err != nil {
				done <- result{k, nil}
				return
			}
			done <- result{k, e}
		}()
	}
	waitForTemp(t, m, 2)

	// 只给会话 B 投递。
	if !m.Temp().Offer(keyB, ev(200)) {
		t.Fatalf("B 的 Offer 应命中")
	}
	select {
	case r := <-done:
		if r.key != keyB {
			t.Fatalf("先被唤醒的应是 B: %+v", r.key)
		}
		if r.ev == nil || r.ev.UserID != 200 {
			t.Fatalf("B 收到的事件不对: %+v", r.ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("B 未被唤醒")
	}
	// A 仍在等待：此时给 A 投递，确认没有串扰。
	if !m.Temp().Offer(keyA, ev(100)) {
		t.Fatalf("A 的 Offer 应命中")
	}
	select {
	case r := <-done:
		if r.key != keyA || r.ev == nil || r.ev.UserID != 100 {
			t.Fatalf("A 收到的事件不对: key=%+v ev=%+v", r.key, r.ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("A 未被唤醒")
	}
}

func Test_F16_AwaitRequiresMatch(t *testing.T) {
	t.Parallel()
	m := New()
	if _, err := m.Await(context.Background(), Key{SelfID: 1}, nil); !errors.Is(err, ErrAwaitNoMatch) {
		t.Fatalf("nil 匹配条件应被拒绝（否则会吞掉该会话全部消息）: %v", err)
	}
}

// Test_F15_ConcurrentOfferAndRegister 覆盖并发安全。
func Test_F15_ConcurrentOfferAndRegister(t *testing.T) {
	t.Parallel()
	tt := NewTempTable()
	key := Key{SelfID: 1, UserID: 100}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				remove := tt.Register(TempRoute{Key: key, Name: "x", Once: true})
				remove()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tt.Offer(key, ev(100))
				tt.Len()
			}
		}()
	}
	wg.Wait()
}

func waitForTemp(t *testing.T, m *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Temp().Len() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待临时路由注册超时: want=%d actual=%d", want, m.Temp().Len())
}

// fakePendingStore 记录会话层发出的在途记录调用。
type fakePendingStore struct {
	saved    []PendingRecord
	finished []string
	failSave bool
}

func (f *fakePendingStore) SavePending(ctx context.Context, r PendingRecord) error {
	if f.failSave {
		return errors.New("store down")
	}
	f.saved = append(f.saved, r)
	return nil
}

func (f *fakePendingStore) FinishPending(ctx context.Context, id, status, note string) error {
	f.finished = append(f.finished, id+"|"+status)
	return nil
}

// Test_F86_RegisterPersistsPending 覆盖"带 Pending 的等待会落盘"。
func Test_F86_RegisterPersistsPending(t *testing.T) {
	t.Parallel()
	ps := &fakePendingStore{}
	tt := NewTempTable().WithPendingStore(ps, nil)
	key := Key{SelfID: 1, GroupID: 2}

	remove := tt.Register(TempRoute{
		Key: key, Name: "await", Once: true, TTL: time.Minute,
		Match:   func(*event.Event) bool { return true },
		Pending: &PendingMeta{ID: "p1", Kind: "await", Payload: "等一条"},
	})
	if len(ps.saved) != 1 || ps.saved[0].ID != "p1" {
		t.Fatalf("应落盘一条在途记录: %+v", ps.saved)
	}
	if ps.saved[0].SessionKey != key.String() {
		t.Fatalf("应记录会话键: %+v", ps.saved[0])
	}
	if ps.saved[0].ExpiresAt <= 0 {
		t.Fatalf("应记录到期时间: %+v", ps.saved[0])
	}

	remove()
	if len(ps.finished) != 1 || !strings.Contains(ps.finished[0], "done") {
		t.Fatalf("移除时应标记完成: %v", ps.finished)
	}

	// 幂等：重复调用移除函数只收尾一次。
	remove()
	if len(ps.finished) != 1 {
		t.Fatalf("移除函数应幂等: %v", ps.finished)
	}
}

// Test_F86_RemoveKeyOrphansPending 覆盖会话回收时的收尾。
func Test_F86_RemoveKeyOrphansPending(t *testing.T) {
	t.Parallel()
	ps := &fakePendingStore{}
	tt := NewTempTable().WithPendingStore(ps, nil)
	key := Key{SelfID: 1, UserID: 9}
	tt.Register(TempRoute{Key: key, Name: "await", Once: true, TTL: time.Minute,
		Pending: &PendingMeta{ID: "p2", Kind: "await"}})

	if n := tt.RemoveKey(key); n != 1 {
		t.Fatalf("应移除 1 条: %d", n)
	}
	if len(ps.finished) != 1 || !strings.Contains(ps.finished[0], "orphaned") {
		t.Fatalf("会话回收应把在途记录标为孤儿: %v", ps.finished)
	}
}

// Test_F86_PersistFailureDoesNotBlockWait 守住"落盘失败不影响等待本身"。
func Test_F86_PersistFailureDoesNotBlockWait(t *testing.T) {
	t.Parallel()
	ps := &fakePendingStore{failSave: true}
	var warned []string
	tt := NewTempTable().WithPendingStore(ps, func(m string) { warned = append(warned, m) })
	key := Key{SelfID: 1, UserID: 9}

	remove := tt.Register(TempRoute{Key: key, Name: "await", Once: true, TTL: time.Minute,
		Match:   func(*event.Event) bool { return true },
		Pending: &PendingMeta{ID: "p3"}})
	defer remove()

	if tt.Len() != 1 {
		t.Fatalf("落盘失败不该影响注册")
	}
	if len(warned) == 0 || !strings.Contains(warned[0], "restart") {
		t.Fatalf("落盘失败必须告警: %v", warned)
	}
}
