package router

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

func mkEvent(t *testing.T, raw string) *event.Event {
	t.Helper()
	return event.NewEvent([]byte(raw))
}

const groupMessage = `{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":1,"sender":{"user_id":20002,"role":"member"},"message":[{"type":"text","data":{"text":"/ping hello"}}]}`

func Test_F11_KeepPrefixSurvivesRouteSwitch(t *testing.T) {
	t.Parallel()
	c := NewCtx(context.Background(), mkEvent(t, groupMessage), nil)
	c.Set(StateKeyKeepPrefix+"x", 1)
	c.Set("plain", 2)
	c.Set(StateKeyCommand, "ping")

	c.ResetForNextRoute()

	if _, ok := c.Get(StateKeyKeepPrefix + "x"); !ok {
		t.Fatalf("keep-prefixed key was cleared: actual=missing expected=present")
	}
	if _, ok := c.Get("plain"); ok {
		t.Fatalf("plain key survived route switch: actual=present expected=cleared")
	}
	if _, ok := c.Get(StateKeyCommand); ok {
		t.Fatalf("command key survived route switch: actual=present expected=cleared")
	}
}

func Test_F11_ContextInterfaceForwardsToInnerContext(t *testing.T) {
	t.Parallel()
	type key struct{}
	inner, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inner = context.WithValue(inner, key{}, "v")

	c := NewCtx(inner, mkEvent(t, groupMessage), nil)

	deadline, ok := c.Deadline()
	if !ok {
		t.Fatalf("Deadline: actual=not ok expected=ok")
	}
	innerDeadline, _ := inner.Deadline()
	if !deadline.Equal(innerDeadline) {
		t.Fatalf("Deadline mismatch: actual=%v expected=%v", deadline, innerDeadline)
	}
	if c.Value(key{}) != "v" {
		t.Fatalf("Value forwarding: actual=%v expected=v", c.Value(key{}))
	}
	if c.Done() == nil {
		t.Fatalf("Done: actual=nil expected=channel")
	}
	if err := c.Err(); err != nil {
		t.Fatalf("Err before cancel: actual=%v expected=nil", err)
	}
}

func Test_F11_MessageStringIsCached(t *testing.T) {
	t.Parallel()
	ev := mkEvent(t, groupMessage)
	c := NewCtx(context.Background(), ev, nil)
	first := c.MessageString()
	if first != "/ping hello" {
		t.Fatalf("MessageString: actual=%q expected=%q", first, "/ping hello")
	}
	// 改动事件消息后仍应返回缓存值（同一次事件内多条规则复用）。
	ev.Message = event.Message{event.Text("changed")}
	if second := c.MessageString(); second != first {
		t.Fatalf("MessageString was not cached: actual=%q expected=%q", second, first)
	}
}

func Test_F11_NilEventIsSafe(t *testing.T) {
	t.Parallel()
	c := NewCtx(context.Background(), nil, nil)
	if got := c.MessageString(); got != "" {
		t.Fatalf("MessageString with nil event: actual=%q expected=empty", got)
	}
	c.Set("k", 1)
	if v, ok := c.GetInt64("k"); !ok || v != 1 {
		t.Fatalf("GetInt64: actual=(%d,%v) expected=(1,true)", v, ok)
	}
	if _, ok := c.GetString("k"); ok {
		t.Fatalf("GetString on int value should fail")
	}
	if _, ok := c.GetBool("k"); ok {
		t.Fatalf("GetBool on int value should fail")
	}
}

func Test_F11_ConcurrentStateAccess(t *testing.T) {
	t.Parallel()
	c := NewCtx(context.Background(), mkEvent(t, groupMessage), nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Set("k"+strconv.Itoa(n), j)
				_, _ = c.Get("k" + strconv.Itoa(n))
				_ = c.Keys()
			}
		}(i)
	}
	wg.Wait()
	if len(c.Keys()) != 20 {
		t.Fatalf("keys: actual=%d expected=20", len(c.Keys()))
	}
}

func Test_F10_Combinators(t *testing.T) {
	t.Parallel()
	c := NewCtx(context.Background(), mkEvent(t, groupMessage), nil)
	tr := func(*Ctx) bool { return true }
	fa := func(*Ctx) bool { return false }

	cases := []struct {
		name string
		rule Rule
		want bool
	}{
		{"and true,true", And(tr, tr), true},
		{"and true,false", And(tr, fa), false},
		{"and false,true", And(fa, tr), false},
		{"and false,false", And(fa, fa), false},
		{"or true,true", Or(tr, tr), true},
		{"or true,false", Or(tr, fa), true},
		{"or false,true", Or(fa, tr), true},
		{"or false,false", Or(fa, fa), false},
		{"not true", Not(tr), false},
		{"not false", Not(fa), true},
		{"all alias", All(tr, tr), true},
		{"empty and", And(), true},
		{"empty or", Or(), false},
	}
	for _, tc := range cases {
		if got := tc.rule(c); got != tc.want {
			t.Fatalf("%s: actual=%v expected=%v", tc.name, got, tc.want)
		}
	}
}

var _ = strings.TrimSpace
