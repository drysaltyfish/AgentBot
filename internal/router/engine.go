package router

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// ErrPanic 表示 Rule/Handler 发生 panic（已被调度层恢复）。
var ErrPanic = errors.New("panic recovered in engine")

// RouteObserver 观测每条路由的匹配与耗时（F-68 的指标接入口）。
//
// 定义在 router 侧，避免 router 依赖具体指标实现；组合根把指标目录适配成这个接口即可。
type RouteObserver interface {
	// RouteMatched 在一条路由执行完毕后调用（含耗时）。
	RouteMatched(route string, d time.Duration)
	// RoutePanicked 在该路由的 Handler 发生 panic（已被恢复）后调用。
	RoutePanicked(route string)
}

// WithObserver 注入路由观测器。
func WithObserver(o RouteObserver) EngineOption { return func(e *Engine) { e.observer = o } }

// EngineOption 配置 Engine。
type EngineOption func(*Engine)

// WithPanicHandler 注入 panic 上报回调（例如写日志）。
func WithPanicHandler(fn func(phase string, recovered any, stack []byte)) EngineOption {
	return func(e *Engine) { e.onPanic = fn }
}

// WithRejectHandler 注入"被 pre/mid/规则拒绝"的回调，保证拒绝可观测。
func WithRejectHandler(fn func(c *Ctx, phase string)) EngineOption {
	return func(e *Engine) { e.onReject = fn }
}

// Engine 把三段钩子与路由表组合成一次事件的调度。
//
// 执行顺序（对每条可能匹配的路由）：
//
//	pre 钩子 -> 路由私有 pre -> 路由 Rules -> mid 钩子 -> Handlers -> post 钩子
//
// 任一 pre/mid/Rule 返回 false 表示"本条路由放弃"，继续尝试下一条路由。
type Engine struct {
	router *Router

	mu       sync.RWMutex
	pre      []Rule
	preNames []string
	mid      []Rule
	post     []Handler
	onPanic  func(phase string, recovered any, stack []byte)
	observer RouteObserver
	onReject func(c *Ctx, phase string)
}

// NewEngine 构造调度器。
func NewEngine(r *Router, opts ...EngineOption) *Engine {
	e := &Engine{router: r}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Router 返回底层路由表。
func (e *Engine) Router() *Router { return e.router }

// UsePre 注册全局 pre 钩子（黑白名单、功能开关、群组过滤）。
func (e *Engine) UsePre(rules ...Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pre = append(e.pre, rules...)
	for range rules {
		e.preNames = append(e.preNames, "")
	}
}

// UsePreNamed 与 UsePre 相同，但给钩子起一个名字。
//
// 名字只用于**顺序断言与诊断**，不影响执行。存在的理由：pre 钩子按注册顺序执行，
// 而顺序是行为的一部分（名单必须先于审查）。只写在注释里的顺序约束没法被检查，
// 改错了照样编译、照样过测试——所以它需要能被读出来断言。
func (e *Engine) UsePreNamed(name string, rule Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pre = append(e.pre, rule)
	e.preNames = append(e.preNames, name)
}

// PreHookNames 返回已注册 pre 钩子的名字（按执行顺序）；未命名的为空串。
//
// 返回值是副本，调用方可以安全保存或改动。
func (e *Engine) PreHookNames() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.preNames...)
}

// UseMid 注册全局 mid 钩子（限速、单飞、并发闸门）。
func (e *Engine) UseMid(rules ...Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mid = append(e.mid, rules...)
}

// UsePost 注册全局 post 钩子（统计、指标、清理）。
func (e *Engine) UsePost(hs ...Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.post = append(e.post, hs...)
}

func (e *Engine) preRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]Rule(nil), e.pre...)
}

func (e *Engine) midRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]Rule(nil), e.mid...)
}

func (e *Engine) postHandlers() []Handler {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]Handler(nil), e.post...)
}

// Dispatch 按优先级依次尝试路由，返回被实际执行的路由数量。
//
// 单条路由的 panic 不会影响其它路由；post 钩子在 Handler panic 后仍会执行。
func (e *Engine) Dispatch(ctx context.Context, ev *event.Event, caller transport.Caller) int {
	c := NewCtx(ctx, ev, caller)
	matched := 0
	for _, rt := range e.router.Snapshot() {
		if rt.removed.Load() {
			continue
		}
		if !KindMatches(rt.kind, ev) {
			continue
		}
		c.ResetForNextRoute()

		if !e.runRules(c, "pre", e.preRules()...) {
			e.reject(c, "pre")
			continue
		}
		if !e.runRules(c, "pre-route", rt.PreRules()...) {
			e.reject(c, "pre-route")
			continue
		}
		if !e.runRules(c, "rules", rt.rules...) {
			e.reject(c, "rules")
			continue
		}
		if !e.runRules(c, "mid", e.midRules()...) {
			e.reject(c, "mid")
			continue
		}

		matched++
		start := time.Now()
		panicked := e.runRoute(c, rt)
		if e.observer != nil {
			e.observer.RouteMatched(rt.name, time.Since(start))
			if panicked {
				e.observer.RoutePanicked(rt.name)
			}
		}

		if rt.IsOnce() {
			e.router.Remove(rt)
		}
		if rt.block || rt.brk {
			break
		}
	}
	return matched
}

func (e *Engine) runRoute(c *Ctx, rt *Route) (panicked bool) {
	// Block: 本条路由执行后停止尝试后续路由。
	// Break: 同上，并且跳过 post 钩子（用于"已充分处理、无需统计"的场景）。
	skipPost := rt.brk
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			e.reportPanic("handler", r)
		}
		if !skipPost {
			e.runPost(c)
		}
	}()
	for _, h := range rt.handlers {
		h(c)
	}
	return panicked
}

func (e *Engine) runPost(c *Ctx) {
	for _, h := range e.postHandlers() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.reportPanic("post", r)
				}
			}()
			h(c)
		}()
	}
}

func (e *Engine) runRules(c *Ctx, phase string, rules ...Rule) (ok bool) {
	ok = true
	defer func() {
		if r := recover(); r != nil {
			e.reportPanic(phase, r)
			ok = false
		}
	}()
	for _, r := range rules {
		if !r(c) {
			return false
		}
	}
	return true
}

func (e *Engine) reportPanic(phase string, recovered any) {
	if e.onPanic != nil {
		e.onPanic(phase, recovered, debug.Stack())
	}
}

func (e *Engine) reject(c *Ctx, phase string) {
	if e.onReject != nil {
		e.onReject(c, phase)
	}
}
