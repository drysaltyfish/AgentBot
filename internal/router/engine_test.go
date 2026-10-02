package router

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

func Test_F13_HookOrderIsPreRulesMidHandlerPost(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)

	var mu sync.Mutex
	var order []string
	rec := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	engine.UsePre(func(*Ctx) bool { rec("pre"); return true })
	engine.UseMid(func(*Ctx) bool { rec("mid"); return true })
	engine.UsePost(func(*Ctx) { rec("post") })

	router.OnMessage(
		func(*Ctx) bool { rec("rules"); return true },
	).UsePre(func(*Ctx) bool { rec("pre-route"); return true }).
		Handle(func(*Ctx) { rec("handler") })

	ev := event.NewEvent([]byte(groupMessage))
	if n := engine.Dispatch(context.Background(), ev, nil); n != 1 {
		t.Fatalf("matched routes: actual=%d expected=1", n)
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	want := "pre,pre-route,rules,mid,handler,post"
	if got != want {
		t.Fatalf("hook order: actual=%s expected=%s", got, want)
	}
}

func Test_F13_PreRejectionSkipsRouteAndIsObservable(t *testing.T) {
	t.Parallel()
	router := NewRouter()

	var mu sync.Mutex
	var rejected []string
	engine := NewEngine(router, WithRejectHandler(func(c *Ctx, phase string) {
		mu.Lock()
		rejected = append(rejected, phase)
		mu.Unlock()
	}))
	engine.UsePre(func(c *Ctx) bool {
		if c.Event != nil && c.Event.GroupID == 30003 {
			return false
		}
		return true
	})

	ran := 0
	router.OnMessage().Priority(PriorityEarly).Handle(func(*Ctx) { ran++ })
	ev := event.NewEvent([]byte(groupMessage))
	if n := engine.Dispatch(context.Background(), ev, nil); n != 0 {
		t.Fatalf("matched: actual=%d expected=0", n)
	}
	if ran != 0 {
		t.Fatalf("handler ran despite pre rejection: actual=%d expected=0", ran)
	}
	mu.Lock()
	got := rejected
	mu.Unlock()
	if len(got) == 0 || got[0] != "pre" {
		t.Fatalf("rejection not observable: actual=%v expected=[pre]", got)
	}
}

func Test_F13_MidRejectionSkipsHandlers(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)
	engine.UseMid(func(*Ctx) bool { return false })

	ran := 0
	router.OnMessage().Handle(func(*Ctx) { ran++ })
	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 0 {
		t.Fatalf("matched: actual=%d expected=0", n)
	}
	if ran != 0 {
		t.Fatalf("handler ran despite mid rejection")
	}
}

func Test_F13_PostRunsEvenWhenHandlerPanics(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	var panics []string
	engine := NewEngine(router, WithPanicHandler(func(phase string, recovered any, stack []byte) {
		panics = append(panics, phase)
	}))

	postRan := false
	engine.UsePost(func(*Ctx) { postRan = true })
	router.OnMessage().Handle(func(*Ctx) { panic("boom") })

	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 1 {
		t.Fatalf("matched: actual=%d expected=1", n)
	}
	if !postRan {
		t.Fatalf("post hook did not run after handler panic")
	}
	if len(panics) != 1 || panics[0] != "handler" {
		t.Fatalf("panic not reported: actual=%v expected=[handler]", panics)
	}
}

func Test_F13_RulePanicDoesNotAffectOtherRoutes(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)

	router.OnMessage(func(*Ctx) bool { panic("rule boom") }).Priority(PriorityEarly).Handle(func(*Ctx) {
		t.Fatalf("handler of panicking route must not run")
	})
	ran := false
	router.OnMessage().Priority(PriorityLate).Handle(func(*Ctx) { ran = true })

	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 1 {
		t.Fatalf("matched: actual=%d expected=1 (panicking route must be skipped)", n)
	}
	if !ran {
		t.Fatalf("later route did not run after an earlier rule panicked")
	}
}

func Test_F13_BlockStopsLaterRoutes(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)

	var ran []string
	router.OnMessage().Priority(PriorityEarly).Named("first").Handle(func(*Ctx) { ran = append(ran, "first") })
	for _, rt := range router.Snapshot() {
		if rt.Name == "first" {
			rt.Block = true
		}
	}
	router.OnMessage().Priority(PriorityLate).Named("second").Handle(func(*Ctx) { ran = append(ran, "second") })

	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 1 {
		t.Fatalf("matched: actual=%d expected=1", n)
	}
	if strings.Join(ran, ",") != "first" {
		t.Fatalf("Block did not stop later routes: actual=%v expected=[first]", ran)
	}
}

func Test_F13_BreakSkipsPostHook(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)
	postRan := false
	engine.UsePost(func(*Ctx) { postRan = true })

	rt := router.OnMessage()
	rt.Handle(func(*Ctx) {})
	rt.Break = true

	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 1 {
		t.Fatalf("matched: actual=%d expected=1", n)
	}
	if postRan {
		t.Fatalf("Break must skip the post hook: actual=ran expected=skipped")
	}
}

func Test_F13_OnceRouteIsRemovedAfterExecution(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)
	router.OnMessage(Always()).Once(true).Handle(func(*Ctx) {})

	if router.Len() != 1 {
		t.Fatalf("setup: actual=%d expected=1", router.Len())
	}
	ev := event.NewEvent([]byte(groupMessage))
	_ = engine.Dispatch(context.Background(), ev, nil)
	if router.Len() != 0 {
		t.Fatalf("Once route not removed: actual=%d expected=0", router.Len())
	}
	if n := engine.Dispatch(context.Background(), ev, nil); n != 0 {
		t.Fatalf("Once route executed twice: actual=%d expected=0", n)
	}
}

func Test_F13_CallerIsInjectedIntoContext(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	engine := NewEngine(router)
	base := &stubCaller{}
	var seen transport.Caller
	router.OnMessage().Handle(func(c *Ctx) { seen = c.Caller() })
	engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), base)
	if seen != transport.Caller(base) {
		t.Fatalf("caller not injected: actual=%v expected=%v", seen, base)
	}
}

type stubCaller struct{}

func (s *stubCaller) Call(ctx context.Context, req transport.Request) (transport.Response, error) {
	return transport.Response{}, nil
}
