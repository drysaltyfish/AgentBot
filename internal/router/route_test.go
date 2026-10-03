package router

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// Test_RouteReadSideAndBuilders 覆盖封装后的读写面：
// 所有可变态只能经构建方法修改，读方法返回稳定的快照值。
func Test_RouteReadSideAndBuilders(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	rt := r.OnMessage(Always()).
		Named("probe").
		Priority(PriorityEarly).
		Once(true).
		Block(true).
		Break(true).
		Handle(func(*Ctx) {}).
		UsePre(func(*Ctx) bool { return true })

	if rt.Kind() != "message" {
		t.Fatalf("Kind: actual=%q expected=message", rt.Kind())
	}
	if rt.Name() != "probe" {
		t.Fatalf("Name: actual=%q expected=probe", rt.Name())
	}
	if rt.Level() != PriorityEarly {
		t.Fatalf("Level: actual=%d expected=%d", rt.Level(), PriorityEarly)
	}
	if !rt.IsOnce() || !rt.IsBlocked() || !rt.SkipsPost() {
		t.Fatalf("bool builders: once=%v blocked=%v skipsPost=%v expected all true", rt.IsOnce(), rt.IsBlocked(), rt.SkipsPost())
	}
	if got := len(rt.Rules()); got != 1 {
		t.Fatalf("Rules len: actual=%d expected=1", got)
	}
	if got := len(rt.Handlers()); got != 1 {
		t.Fatalf("Handlers len: actual=%d expected=1", got)
	}
	if got := len(rt.PreRules()); got != 1 {
		t.Fatalf("PreRules len: actual=%d expected=1", got)
	}
}

// Test_RouteAccessorsReturnCopies 保证自省读到的切片是副本，
// 外部改写不会污染路由内部状态。
func Test_RouteAccessorsReturnCopies(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	rt := r.OnMessage(Always()).Handle(func(*Ctx) {})

	rules := rt.Rules()
	rules[0] = Never()
	handlers := rt.Handlers()
	handlers[0] = func(*Ctx) { t.Fatalf("mutated handler copy was used") }
	pre := rt.PreRules()
	_ = pre

	if len(rt.Rules()) != 1 || !rt.Rules()[0](NewCtx(context.Background(), event.NewEvent([]byte(groupMessage)), nil)) {
		t.Fatalf("Rules() copy mutation leaked into Route")
	}
	rt.Handlers()[0](NewCtx(context.Background(), event.NewEvent([]byte(groupMessage)), nil))
}

// Test_RoutePriorityBuilderReordersAfterRegistration 保证注册后调用
// Priority 仍会触发 Router 重排序（markDirty），而不是仅改字段。
func Test_RoutePriorityBuilderReordersAfterRegistration(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	a := r.OnMessage().Named("A").Priority(PriorityLate)
	r.OnMessage().Named("B").Priority(PriorityNormal)

	if got := routeNames(r.Routes()); got[0] != "B" || got[1] != "A" {
		t.Fatalf("initial order: actual=%v expected=[B A]", got)
	}
	a.Priority(PriorityFirst)
	if got := routeNames(r.Routes()); got[0] != "A" || got[1] != "B" {
		t.Fatalf("order after Priority: actual=%v expected=[A B]", got)
	}
}

// Test_PredicateRulePlusAlwaysRecordRoute 是本包对 C3 的目标用法：
// 一条谓词路由负责回复策略，一条 Always 路由只做记录；谓词不命中时
// 记录仍然发生，且拒绝可通过 WithRejectHandler 观测。
func Test_PredicateRulePlusAlwaysRecordRoute(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	var rejected []string
	engine := NewEngine(r, WithRejectHandler(func(_ *Ctx, phase string) {
		rejected = append(rejected, phase)
	}))

	replied, recorded := 0, 0
	r.OnMessage(Keyword("/ping")).Named("reply").
		Priority(PriorityEarly).
		Handle(func(*Ctx) { replied++ })
	r.OnMessage(Always()).Named("record").
		Priority(PriorityLate).
		Handle(func(*Ctx) { recorded++ })

	// 不命中谓词：只记录，不回复。
	plain := event.NewEvent([]byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":9,"message":[{"type":"text","data":{"text":"hello"}}]}`))
	if n := engine.Dispatch(context.Background(), plain, nil); n != 1 {
		t.Fatalf("plain dispatch matched: actual=%d expected=1", n)
	}
	if replied != 0 || recorded != 1 {
		t.Fatalf("plain: replied=%d recorded=%d expected 0/1", replied, recorded)
	}
	if len(rejected) != 1 || rejected[0] != "rules" {
		t.Fatalf("rejection phases: actual=%v expected=[rules]", rejected)
	}

	// 命中谓词：回复与记录都发生。
	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 2 {
		t.Fatalf("matching dispatch matched: actual=%d expected=2", n)
	}
	if replied != 1 || recorded != 2 {
		t.Fatalf("matching: replied=%d recorded=%d expected 1/2", replied, recorded)
	}
}

// Test_ReplyPolicyBlockStopsTheRecordRoute 锁定组合根实际使用的注册形态：
// 回复路由命中后必须 Block。否则"只记录"的兜底路由会对同一条消息再入队一次，
// 造成重复记录（甚至重复回复）——这正是把策略从 handler 分支搬到路由时最容易踩的坑。
func Test_ReplyPolicyBlockStopsTheRecordRoute(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	engine := NewEngine(r)

	replied, recorded := 0, 0
	r.OnMessage(Keyword("/ping")).Named("reply").
		Priority(PriorityEarly).
		Block(true).
		Handle(func(*Ctx) { replied++ })
	r.OnMessage(Always()).Named("record").
		Priority(PriorityLate).
		Handle(func(*Ctx) { recorded++ })

	// 命中：只走回复路由，兜底记录路由被 Block 拦住。
	if n := engine.Dispatch(context.Background(), event.NewEvent([]byte(groupMessage)), nil); n != 1 {
		t.Fatalf("matching dispatch matched: actual=%d expected=1（Block 未生效）", n)
	}
	if replied != 1 || recorded != 0 {
		t.Fatalf("matching: replied=%d recorded=%d expected 1/0", replied, recorded)
	}

	// 未命中：谓词拒绝后仍要落到记录路由。
	plain := event.NewEvent([]byte(`{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":9,"message":[{"type":"text","data":{"text":"hello"}}]}`))
	if n := engine.Dispatch(context.Background(), plain, nil); n != 1 {
		t.Fatalf("plain dispatch matched: actual=%d expected=1", n)
	}
	if replied != 1 || recorded != 1 {
		t.Fatalf("plain: replied=%d recorded=%d expected 1/1", replied, recorded)
	}
}
