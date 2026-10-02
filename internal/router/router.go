package router

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// 语义化优先级常量：数值小者先执行。
const (
	PriorityFirst  = 0
	PriorityEarly  = 10
	PriorityNormal = 50
	PriorityLate   = 90
	PriorityLast   = 100
)

// Route 是一条路由。
//
// 注意：priority / once 用非导出字段承载，以便同时提供链式方法
// Priority(p) / Once(bool)（F-08 的链式注册 API）。
type Route struct {
	Kind     string
	Rules    []Rule
	Handlers []Handler
	Block    bool
	Break    bool
	Name     string

	priority int
	once     bool
	pre      []Rule
	expire   time.Duration
	created  time.Time
	used     atomic.Bool
	removed  atomic.Bool
	owner    *Router
}

// Priority 设置优先级并让路由表重新排序（递增 epoch）。
func (rt *Route) Priority(p int) *Route {
	rt.priority = p
	if rt.owner != nil {
		rt.owner.markDirty()
	}
	return rt
}

// Level 返回当前优先级。
func (rt *Route) Level() int { return rt.priority }

// Once 设置"匹配执行后自动注销"。
func (rt *Route) Once(on bool) *Route {
	rt.once = on
	return rt
}

// IsOnce 报告是否为一次性路由。
func (rt *Route) IsOnce() bool { return rt.once }

// Expire 设置注册后的存活期（F-15 的过期清理使用）。
func (rt *Route) Expire(d time.Duration) *Route {
	rt.expire = d
	return rt
}

// ExpiresAt 返回过期时刻；未设置过期时返回零值。
func (rt *Route) ExpiresAt() time.Time {
	if rt.expire <= 0 {
		return time.Time{}
	}
	return rt.created.Add(rt.expire)
}

// Named 设置路由名（便于 /routes 与管理命令展示）。
//
// 重名不报错，但会通过 WithWarnFunc 告警（F-08）。
func (rt *Route) Named(name string) *Route {
	rt.Name = name
	if rt.owner != nil {
		rt.owner.noteName(name)
	}
	return rt
}

// Handle 追加处理器。
func (rt *Route) Handle(hs ...Handler) *Route {
	rt.Handlers = append(rt.Handlers, hs...)
	return rt
}

// UseRules 追加该路由私有规则。
func (rt *Route) UseRules(rs ...Rule) *Route {
	rt.Rules = append(rt.Rules, rs...)
	return rt
}

// UsePre 追加只对该路由生效的 pre 钩子。
func (rt *Route) UsePre(rs ...Rule) *Route {
	rt.pre = append(rt.pre, rs...)
	return rt
}

// PreRules 返回该路由的私有 pre 钩子。
func (rt *Route) PreRules() []Rule {
	out := make([]Rule, len(rt.pre))
	copy(out, rt.pre)
	return out
}

// Used 报告该路由是否已被执行过。
func (rt *Route) Used() bool { return rt.used.Load() }

// Removed 报告该路由是否已注销。
func (rt *Route) Removed() bool { return rt.removed.Load() }

// RouteInfo 是路由自省信息。
type RouteInfo struct {
	Name     string
	Kind     string
	Priority int
	Once     bool
	Rules    int
	Handlers int
}

// Option 配置 Router。
type Option func(*Router)

// WithWarnFunc 注入告警回调（例如重名路由）。
func WithWarnFunc(fn func(string)) Option {
	return func(r *Router) { r.warn = fn }
}

// WithClock 注入时间源（测试用）。
func WithClock(now func() time.Time) Option {
	return func(r *Router) {
		if now != nil {
			r.now = now
		}
	}
}

// Router 是实例化路由注册表；禁止包级全局注册表。
type Router struct {
	mu     sync.RWMutex
	routes []*Route
	names  map[string]int
	warn   func(string)
	now    func() time.Time

	epoch     atomic.Uint64
	snap      atomic.Pointer[[]*Route]
	snapEpoch atomic.Uint64
}

// NewRouter 构造路由表。
func NewRouter(opts ...Option) *Router {
	r := &Router{names: map[string]int{}, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	return r
}

// On 注册一条通用路由：kind 支持 "message" / "message/group" / "notice/notify/poke"。
func (r *Router) On(kind string, rules ...Rule) *Route {
	rt := &Route{Kind: kind, priority: PriorityNormal, created: r.now(), owner: r}
	rt.Rules = append(rt.Rules, rules...)
	r.add(rt)
	return rt
}

// OnMessage 注册 message 路由。
func (r *Router) OnMessage(rules ...Rule) *Route { return r.On("message", rules...) }

// OnNotice 注册 notice 路由。
func (r *Router) OnNotice(rules ...Rule) *Route { return r.On("notice", rules...) }

// OnRequest 注册 request 路由。
func (r *Router) OnRequest(rules ...Rule) *Route { return r.On("request", rules...) }

// OnMeta 注册 meta 路由。
func (r *Router) OnMeta(rules ...Rule) *Route { return r.On("meta", rules...) }

// OnCommand 注册命令路由。
func (r *Router) OnCommand(prefix string, cmds ...string) *Route {
	return r.OnMessage(Command(prefix, cmds...))
}

// OnPrefix 注册前缀路由。
func (r *Router) OnPrefix(ps ...string) *Route { return r.OnMessage(Prefix(ps...)) }

// OnSuffix 注册后缀路由。
func (r *Router) OnSuffix(ss ...string) *Route { return r.OnMessage(Suffix(ss...)) }

// OnRegex 注册正则路由。
func (r *Router) OnRegex(pattern string) *Route { return r.OnMessage(Regex(pattern)) }

// OnKeyword 注册关键词路由。
func (r *Router) OnKeyword(ks ...string) *Route { return r.OnMessage(Keyword(ks...)) }

// OnFullMatch 注册全等路由。
func (r *Router) OnFullMatch(ss ...string) *Route { return r.OnMessage(FullMatch(ss...)) }

// OnAtMe 注册"@我"路由。
func (r *Router) OnAtMe() *Route { return r.OnMessage(AtMe()) }

func (r *Router) add(rt *Route) {
	r.mu.Lock()
	rt.owner = r
	r.routes = append(r.routes, rt)
	r.mu.Unlock()
	if rt.Name != "" {
		r.noteName(rt.Name)
	}
	r.markDirty()
}

// noteName 维护重名计数并对重复名字告警一次（计数 1 -> 2 时）。
func (r *Router) noteName(name string) {
	if name == "" {
		return
	}
	r.mu.Lock()
	r.names[name]++
	n := r.names[name]
	warn := r.warn
	r.mu.Unlock()
	if n > 1 && warn != nil {
		warn("duplicate route name: " + name)
	}
}

func (r *Router) markDirty() {
	r.mu.Lock()
	sort.SliceStable(r.routes, func(i, j int) bool { return r.routes[i].priority < r.routes[j].priority })
	r.mu.Unlock()
	r.epoch.Add(1)
}

// Remove 注销路由；对已注销的路由重复调用是幂等的。
func (r *Router) Remove(rt *Route) {
	if rt == nil || !rt.removed.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	kept := r.routes[:0]
	for _, cur := range r.routes {
		if cur == rt {
			continue
		}
		kept = append(kept, cur)
	}
	r.routes = kept
	if rt.Name != "" && r.names[rt.Name] > 0 {
		r.names[rt.Name]--
	}
	r.mu.Unlock()
	r.markDirty()
}

// Len 返回当前路由数量。
func (r *Router) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// Routes 返回路由自省信息（按执行顺序）。
func (r *Router) Routes() []RouteInfo {
	snap := r.Snapshot()
	out := make([]RouteInfo, 0, len(snap))
	for _, rt := range snap {
		out = append(out, RouteInfo{
			Name:     rt.Name,
			Kind:     rt.Kind,
			Priority: rt.priority,
			Once:     rt.once,
			Rules:    len(rt.Rules),
			Handlers: len(rt.Handlers),
		})
	}
	return out
}

// Snapshot 返回按优先级排序的只读路由快照。
//
// 快路径完全无锁：epoch 未变时直接复用已有快照切片（F-12）。
func (r *Router) Snapshot() []*Route {
	if r.snapEpoch.Load() == r.epoch.Load() {
		if p := r.snap.Load(); p != nil {
			return *p
		}
	}
	r.mu.Lock()
	if r.snapEpoch.Load() != r.epoch.Load() || r.snap.Load() == nil {
		cp := make([]*Route, len(r.routes))
		copy(cp, r.routes)
		r.snap.Store(&cp)
		r.snapEpoch.Store(r.epoch.Load())
	}
	snap := *r.snap.Load()
	r.mu.Unlock()
	return snap
}

// Epoch 返回当前的注册表版本号。
func (r *Router) Epoch() uint64 { return r.epoch.Load() }

// KindMatches 判断事件是否命中 kind 模式（按 "/" 分段逐级比较）。
func KindMatches(pattern string, ev *event.Event) bool {
	if ev == nil {
		return false
	}
	if pattern == "" {
		return true
	}
	// 用定长数组避免热路径分配（BenchmarkRouteMatch 关注 allocs/op）。
	var have [3]string
	n := 1
	have[0] = string(ev.Kind)
	if ev.Sub != "" {
		have[n] = ev.Sub
		n++
	}
	if ev.SubSub != "" {
		have[n] = ev.SubSub
		n++
	}

	seg := 0
	rest := pattern
	for {
		part := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			part, rest = rest[:i], rest[i+1:]
		} else {
			rest = ""
		}
		if seg >= n || part != have[seg] {
			return false
		}
		seg++
		if rest == "" {
			return true
		}
	}
}
