package router

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
)

func int64Key(c *Ctx) int64 { return c.Event.UserID }

// Test_F17_RejectsSecondInflightCall 覆盖验收：并发两次同 key，第二次被拒绝且回调触发。
func Test_F17_RejectsSecondInflightCall(t *testing.T) {
	t.Parallel()
	sf := NewSingleflight[int64](int64Key)
	var rejected atomic.Int64
	sf.OnReject(func(*Ctx) { rejected.Add(1) })

	r := NewRouter()
	engine := NewEngine(r)
	engine.UseMid(sf.Rule())
	engine.UsePost(sf.Release())
	r.OnMessage(Always()).Named("only").Handle(func(*Ctx) {})

	ev := event.NewEvent([]byte(groupMessage))

	// 直接调用 Rule 模拟"同一时刻有第二个请求"：占位存在即拒绝。
	first := NewCtx(context.Background(), ev, nil)
	if !sf.Rule()(first) {
		t.Fatalf("首次调用应放行")
	}
	second := NewCtx(context.Background(), ev, nil)
	if sf.Rule()(second) {
		t.Fatalf("同 key 的第二次调用应被拒绝")
	}
	if got := rejected.Load(); got != 1 {
		t.Fatalf("拒绝回调次数: actual=%d expected=1", got)
	}

	// post 之后占位释放，再来一次应放行。
	sf.Release()(first)
	third := NewCtx(context.Background(), ev, nil)
	if !sf.Rule()(third) {
		t.Fatalf("释放后同 key 应再次放行")
	}
	sf.Release()(third)

	// 引擎路径同样应当成对：mid 占位、post 释放。
	if n := engine.Dispatch(context.Background(), ev, nil); n != 1 {
		t.Fatalf("dispatch matched: actual=%d expected=1", n)
	}
	if got := sf.Inflight(); got != 0 {
		t.Fatalf("post 之后占位应清空: actual=%d", got)
	}
}

// Test_F17_PanicStillReleases 覆盖边界：Handler panic 后占位必须已释放。
func Test_F17_PanicStillReleases(t *testing.T) {
	t.Parallel()
	sf := NewSingleflight[int64](int64Key)

	r := NewRouter()
	engine := NewEngine(r, WithPanicHandler(func(string, any, []byte) {}))
	engine.UseMid(sf.Rule())
	engine.UsePost(sf.Release())

	var boom atomic.Bool
	boom.Store(true)
	r.OnMessage(Always()).Named("flaky").Handle(func(*Ctx) {
		if boom.Load() {
			panic("handler exploded")
		}
	})

	ev := event.NewEvent([]byte(groupMessage))
	engine.Dispatch(context.Background(), ev, nil) // 第一次 panic（已被恢复）

	if got := sf.Inflight(); got != 0 {
		t.Fatalf("panic 后占位应已释放: actual=%d", got)
	}
	if !sf.Rule()(NewCtx(context.Background(), ev, nil)) {
		t.Fatalf("占位释放后同 key 应能通过")
	}
}

// Test_F17_DistinctKeysDoNotBlockEachOther 覆盖"只挡同一个 key"。
func Test_F17_DistinctKeysDoNotBlockEachOther(t *testing.T) {
	t.Parallel()
	sf := NewSingleflight[int64](int64Key)
	a := NewCtx(context.Background(), event.NewEvent([]byte(groupMessage)), nil)
	b := NewCtx(context.Background(), event.NewEvent([]byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":77777,"group_id":30003,"message_id":2,"message":[{"type":"text","data":{"text":"hi"}}]}`)), nil)

	if !sf.Rule()(a) || !sf.Rule()(b) {
		t.Fatalf("不同 key 不应互相阻塞")
	}
	if got := sf.Inflight(); got != 2 {
		t.Fatalf("占位数: actual=%d expected=2", got)
	}
	sf.Release()(a)
	sf.Release()(b)
}
