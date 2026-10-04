package main

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/reload"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// rateLimitSet 是一组已构造好的限速规则（按用户、按群各一条）。
type rateLimitSet struct {
	userRule  router.Rule
	groupRule router.Rule
}

// rateLimitState 持有当前生效的限速规则，支持运行时原子替换（F-24）。
//
// 为什么持有"规则"而不是"管理器"：路由引擎的 mid 钩子在启动时注册，之后无法替换。
// 于是注册一次，规则内部每次解引用当前值——热加载因此只是一次原子写，
// 请求路径只多一次 atomic.Load。反过来（重建中间件）做不到。
type rateLimitState struct {
	set atomic.Pointer[rateLimitSet]
}

// newRateLimitState 按当前配置构造；未启用时规则为空（全部放行）。
func newRateLimitState(cfg config.RateLimit, onUser, onGroup func(*router.Ctx)) *rateLimitState {
	s := &rateLimitState{}
	s.Store(cfg, onUser, onGroup)
	return s
}

// Store 原子替换当前规则集；未启用时换成 nil（规则随即恒真）。
//
// 注意这里**每次重建管理器**而不是改参数：限速器的桶里存着已消耗的令牌，
// 就地改 rate/burst 会让"改配置"变成一次隐式清空或放大——两种都不是用户预期的。
// 新参数从下一次请求开始按新桶计。
func (s *rateLimitState) Store(cfg config.RateLimit, onUser, onGroup func(*router.Ctx)) {
	if s == nil || !cfg.EffectiveEnabled() {
		if s != nil {
			s.set.Store(nil)
		}
		return
	}
	user := router.NewLimiterManager[int64](
		float64(cfg.EffectiveUserPerMinute())/60, float64(cfg.EffectiveUserBurst()))
	group := router.NewLimiterManager[int64](
		float64(cfg.EffectiveGroupPerMinute())/60, float64(cfg.EffectiveGroupBurst()))
	s.set.Store(&rateLimitSet{
		userRule:  user.Rule(func(c *router.Ctx) int64 { return c.Event.UserID }, onUser),
		groupRule: group.Rule(func(c *router.Ctx) int64 { return c.Event.GroupID }, onGroup),
	})
}

// Rules 返回挂到引擎上的用户/群规则。
//
// 无论当时是否启用都必须注册：否则"先关后开"的热加载会打开一堆没有限速的规则，
// 而那正是运维最容易踩到的顺序。
func (s *rateLimitState) Rules() (user, group router.Rule) {
	user = func(c *router.Ctx) bool {
		if set := s.set.Load(); set != nil && set.userRule != nil {
			return set.userRule(c)
		}
		return true
	}
	group = func(c *router.Ctx) bool {
		if set := s.set.Load(); set != nil && set.groupRule != nil {
			return set.groupRule(c)
		}
		return true
	}
	return user, group
}

// watchRateLimit 监听配置文件并按新参数重建限速规则（F-24）。
//
// 解析或校验失败时保留旧参数并告警：把限速"重载没了"远比"多限一会儿"危险。
func watchRateLimit(ctx context.Context, path string, state *rateLimitState, onUser, onGroup func(*router.Ctx), lg *observe.Logger) *reload.Watcher[*config.Config] {
	path = strings.TrimSpace(path)
	if path == "" || state == nil {
		return nil
	}
	rlog := lg.Component("ratelimit")
	var w *reload.Watcher[*config.Config]
	w = reload.New([]string{path}, func() (*config.Config, error) {
		fresh, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		if err := fresh.Validate(); err != nil {
			return nil, err
		}
		return fresh, nil
	}, reload.Options{
		OnSwap: func(version uint64, _ string) {
			fresh, ok := w.Current()
			if !ok {
				return
			}
			state.Store(fresh.RateLimit, onUser, onGroup)
			rlog.Info("rate limit parameters reloaded",
				"version", version, "enabled", fresh.RateLimit.EffectiveEnabled(),
				"user_per_minute", fresh.RateLimit.EffectiveUserPerMinute(),
				"user_burst", fresh.RateLimit.EffectiveUserBurst(),
				"group_per_minute", fresh.RateLimit.EffectiveGroupPerMinute(),
				"group_burst", fresh.RateLimit.EffectiveGroupBurst())
		},
		Warn: func(err error) {
			rlog.Warn("rate limit reload failed; keeping the previous parameters", "error", err)
		},
	})
	w.Start(ctx)
	return w
}
