package router

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
)

func routeNames(info []RouteInfo) []string {
	out := make([]string, 0, len(info))
	for _, i := range info {
		out = append(out, i.Name)
	}
	return out
}

func Test_F08_TwoRoutersAreIsolated(t *testing.T) {
	t.Parallel()
	a := NewRouter()
	b := NewRouter()
	a.OnMessage().Named("only-a")
	b.OnMessage().Named("only-b").Named("only-b2")

	if got := routeNames(a.Routes()); strings.Join(got, ",") != "only-a" {
		t.Fatalf("router a: actual=%v expected=[only-a]", got)
	}
	if got := routeNames(b.Routes()); strings.Join(got, ",") != "only-b2" {
		t.Fatalf("router b: actual=%v expected=[only-b2]", got)
	}
}

func Test_F08_RoutesIntrospection(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	r.On("message/group", Always()).Named("cmd").Priority(PriorityEarly).Handle(func(*Ctx) {})
	info := r.Routes()
	if len(info) != 1 {
		t.Fatalf("route count: actual=%d expected=1", len(info))
	}
	got := info[0]
	if got.Name != "cmd" || got.Kind != "message/group" || got.Priority != PriorityEarly || got.Once || got.Handlers != 1 || got.Rules != 1 {
		t.Fatalf("RouteInfo: actual=%+v", got)
	}
}

func Test_F08_DuplicateNameWarns(t *testing.T) {
	t.Parallel()
	var warned []string
	r := NewRouter(WithWarnFunc(func(msg string) { warned = append(warned, msg) }))
	r.OnMessage().Named("dup")
	r.OnMessage().Named("dup")
	if len(warned) == 0 {
		t.Fatalf("duplicate name did not warn: actual=0 warnings")
	}
	if r.Len() != 2 {
		t.Fatalf("duplicate names must not panic or drop routes: actual=%d expected=2", r.Len())
	}
}

func Test_F08_ConvenienceTriggersRegisterMessageRoutes(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	r.OnPrefix("/x")
	r.OnSuffix("!")
	r.OnRegex("^a+$")
	r.OnKeyword("hi")
	r.OnFullMatch("exact")
	r.OnAtMe()
	r.OnCommand("/", "ping")
	if r.Len() != 7 {
		t.Fatalf("registered routes: actual=%d expected=7", r.Len())
	}
	for _, info := range r.Routes() {
		if info.Kind != "message" {
			t.Fatalf("convenience trigger kind: actual=%q expected=message", info.Kind)
		}
	}
}

func Test_F08_KindMatchingForms(t *testing.T) {
	t.Parallel()
	ev := event.NewEvent([]byte(groupMessage))
	sent := event.NewEvent([]byte(`{"post_type":"message_sent","message_type":"group","sub_type":"normal","self_id":10001,"user_id":10001,"group_id":30003,"message_id":2}`))

	cases := []struct {
		pattern string
		ev      *event.Event
		want    bool
	}{
		{"", ev, true},
		{"message", ev, true},
		{"message/group", ev, true},
		{"message/group/normal", ev, true},
		{"message/private", ev, false},
		{"notice", ev, false},
		{"message/group/normal/deep", ev, false},
		{"message", sent, true},
	}
	for _, tc := range cases {
		if got := KindMatches(tc.pattern, tc.ev); got != tc.want {
			t.Fatalf("KindMatches(%q): actual=%v expected=%v", tc.pattern, got, tc.want)
		}
	}
	if KindMatches("message", nil) {
		t.Fatalf("KindMatches with nil event must be false")
	}
}

func Test_F09_StablePriorityOrder(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	r.OnMessage().Named("A").Priority(PriorityNormal)
	r.OnMessage().Named("B").Priority(PriorityEarly)
	r.OnMessage().Named("C").Priority(PriorityNormal)

	want := "B,A,C"
	if got := strings.Join(routeNames(r.Routes()), ","); got != want {
		t.Fatalf("execution order: actual=%s expected=%s", got, want)
	}
}

func Test_F09_OrderStableAfterChurn(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	for i := 0; i < 30; i++ {
		r.OnMessage().Named("r" + strconv.Itoa(i)).Priority(PriorityNormal)
	}
	// 删掉一半再补回来，同优先级仍应保持注册顺序。
	for i := 0; i < 30; i += 2 {
		for _, info := range r.Routes() {
			if info.Name == "r"+strconv.Itoa(i) {
				// 通过路由表快照找到并删除
				for _, rt := range r.Snapshot() {
					if rt.Name() == "r"+strconv.Itoa(i) {
						r.Remove(rt)
					}
				}
			}
		}
	}
	if r.Len() != 15 {
		t.Fatalf("after churn: actual=%d expected=15", r.Len())
	}
	got := routeNames(r.Routes())
	for i := 1; i < len(got); i++ {
		prev, _ := strconv.Atoi(strings.TrimPrefix(got[i-1], "r"))
		cur, _ := strconv.Atoi(strings.TrimPrefix(got[i], "r"))
		if prev >= cur {
			t.Fatalf("same-priority order not stable: actual=%v", got)
		}
	}
}

func Test_F09_EmptyRouterIsSafe(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	if r.Len() != 0 {
		t.Fatalf("empty router: actual=%d expected=0", r.Len())
	}
	if info := r.Routes(); len(info) != 0 {
		t.Fatalf("empty Routes(): actual=%d expected=0", len(info))
	}
	if snap := r.Snapshot(); len(snap) != 0 {
		t.Fatalf("empty Snapshot(): actual=%d expected=0", len(snap))
	}
}

func Test_F12_SnapshotReusedUntilEpochChanges(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	r.OnMessage().Named("a")

	s1 := r.Snapshot()
	s2 := r.Snapshot()
	if len(s1) != 1 || len(s2) != 1 {
		t.Fatalf("snapshot sizes: actual=(%d,%d) expected=(1,1)", len(s1), len(s2))
	}
	if &s1[0] != &s2[0] {
		t.Fatalf("snapshot was rebuilt although epoch did not change")
	}

	before := r.Epoch()
	r.OnMessage().Named("b")
	if r.Epoch() == before {
		t.Fatalf("epoch did not advance on registration")
	}
	s3 := r.Snapshot()
	if len(s3) != 2 {
		t.Fatalf("snapshot after registration: actual=%d expected=2", len(s3))
	}
}

func Test_F12_RemoveIsIdempotentAndInvalidatesSnapshot(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	rt := r.OnMessage().Named("a")
	r.Remove(rt)
	r.Remove(rt)
	if r.Len() != 0 {
		t.Fatalf("after remove: actual=%d expected=0", r.Len())
	}
	if !rt.Removed() {
		t.Fatalf("Removed(): actual=false expected=true")
	}
	if len(r.Snapshot()) != 0 {
		t.Fatalf("snapshot still contains removed route")
	}
}

func Test_F12_ConcurrentRegisterAndMatch(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	ev := event.NewEvent([]byte(groupMessage))
	c := NewCtx(context.Background(), ev, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			r.OnMessage(Keyword("k" + strconv.Itoa(i)))
		}
	}()
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				for _, rt := range r.Snapshot() {
					if !KindMatches(rt.kind, ev) {
						continue
					}
					for _, rule := range rt.rules {
						if !rule(c) {
							break
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	if r.Len() != 400 {
		t.Fatalf("routes: actual=%d expected=400", r.Len())
	}
}

func BenchmarkRouteMatch(b *testing.B) {
	r := NewRouter()
	for i := 0; i < 1000; i++ {
		r.OnMessage(Keyword("k" + strconv.Itoa(i)))
	}
	ev := event.NewEvent([]byte(groupMessage))
	c := NewCtx(context.Background(), ev, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, rt := range r.Snapshot() {
			if !KindMatches(rt.kind, ev) {
				continue
			}
			for _, rule := range rt.rules {
				if !rule(c) {
					break
				}
			}
		}
	}
}

var _ = fmt.Sprintf
