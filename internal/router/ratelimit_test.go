package router

import (
	"context"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// Test_F18_BurstThenRefill 覆盖 F-18 的验收：burst=3、rate=1/s 时连续 3 次通过，
// 第 4 次拒绝；等 1s 后恢复 1 个令牌。
func Test_F18_BurstThenRefill(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	l := NewLimiter(1, 3).WithClock(func() time.Time { return now })

	for i := 1; i <= 3; i++ {
		if !l.Allow() {
			t.Fatalf("第 %d 次应在 burst 内通过", i)
		}
	}
	if l.Allow() {
		t.Fatalf("第 4 次应被拒绝：burst 已用尽")
	}

	now = now.Add(time.Second)
	if !l.Allow() {
		t.Fatalf("等待 1s 后应恢复 1 个令牌")
	}
	if l.Allow() {
		t.Fatalf("恢复的令牌只够再用一次")
	}
}

// Test_F18_ClockBackwardsDoesNotInflate 覆盖边界：时间回拨不得补币，也不得出现负 token。
func Test_F18_ClockBackwardsDoesNotInflate(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	l := NewLimiter(1, 2).WithClock(func() time.Time { return now })

	if !l.Allow() {
		t.Fatalf("初始第一个令牌应可用")
	}
	if !l.Allow() {
		t.Fatalf("初始第二个令牌应可用")
	}
	if l.Allow() {
		t.Fatalf("桶已空")
	}

	// 回拨 10 分钟：既不该补币，也不该让 token 变负而"欠账"。
	now = now.Add(-10 * time.Minute)
	if l.Allow() {
		t.Fatalf("时间回拨不得凭空补币")
	}
	now = now.Add(10*time.Minute + time.Second)
	if !l.Allow() {
		t.Fatalf("回到正常时间后应按真实经过时间补币")
	}
}

// Test_F18_TTLReclaimsIdleKeys 覆盖边界：10 万个不同 key 后，TTL 到期应回收。
func Test_F18_TTLReclaimsIdleKeys(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	m := NewLimiterManager[int](1, 3).WithClock(func() time.Time { return now })

	const keys = 100000
	for i := 0; i < keys; i++ {
		m.Allow(i, 1)
	}
	if got := m.Len(); got != keys {
		t.Fatalf("活跃 key 数: actual=%d expected=%d", got, keys)
	}

	// TTL = burst/rate*3 = 9s；前进超过 TTL 后，下一次 Allow 触发清扫。
	now = now.Add(10 * time.Second)
	if !m.Allow(keys+1, 1) {
		t.Fatalf("新 key 应可用")
	}
	if got := m.Len(); got > 2 {
		t.Fatalf("空闲 key 未被回收: actual=%d expected<=2", got)
	}
}

// Test_F18_RuleRejectsWhenLimited 覆盖挂载方式：作为 mid 钩子时超限即拒绝本轮路由。
func Test_F18_RuleRejectsWhenLimited(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	m := NewLimiterManager[int64](1, 1).WithClock(func() time.Time { return now })

	r := NewRouter()
	engine := NewEngine(r)
	engine.UseMid(m.Rule(func(c *Ctx) int64 { return c.Event.UserID }, nil))

	ran := 0
	r.OnMessage(Always()).Named("only").Handle(func(*Ctx) { ran++ })

	ev := event.NewEvent([]byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":1,"message":[{"type":"text","data":{"text":"hi"}}]}`))

	if n := engine.Dispatch(context.Background(), ev, nil); n != 1 || ran != 1 {
		t.Fatalf("首次应通过: matched=%d ran=%d", n, ran)
	}
	if n := engine.Dispatch(context.Background(), ev, nil); n != 0 || ran != 1 {
		t.Fatalf("桶已空，应被 mid 钩子拒绝: matched=%d ran=%d", n, ran)
	}

	now = now.Add(time.Second)
	if n := engine.Dispatch(context.Background(), ev, nil); n != 1 || ran != 2 {
		t.Fatalf("补充令牌后应恢复: matched=%d ran=%d", n, ran)
	}
}
