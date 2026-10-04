package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/policy"
	"github.com/drysaltyfish/agentbot/internal/reload"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// loadPolicy 加载权限表（F-53）。
//
// 文件为空时用内置默认表；配置了文件但读不到或解析失败则**启动失败**——
// 权限表是安全边界，静默退回默认表等于悄悄换了一套权限。
func loadPolicy(cfg *config.Config, lg *observe.Logger) (*policy.Policy, error) {
	path := strings.TrimSpace(cfg.Policy.File)
	if path == "" {
		p, err := policy.LoadDefault()
		if err != nil {
			return nil, fmt.Errorf("load builtin policy: %w", err)
		}
		return p, nil
	}
	p, err := policy.LoadFile(path)
	if err != nil {
		// 文件不存在 ≠ 配置错误：F-53 把内置版本定义为**基线**、外部文件定义为**覆盖**，
		// 而默认配置本来就指向 actions.yaml（多数部署不会放这个文件）。
		// 解析/校验失败才是真配置错误，必须启动失败。
		if errors.Is(err, fs.ErrNotExist) {
			if lg != nil {
				lg.Component("policy").Info("policy file not found; using the builtin table", "path", path)
			}
			return policy.LoadDefault()
		}
		return nil, fmt.Errorf("load policy %s: %w", path, err)
	}
	if lg != nil {
		lg.Component("policy").Info("policy table loaded",
			"path", path, "actions", len(p.Actions()), "roles", len(p.Roles()))
	}
	return p, nil
}

// 角色解析见 access.go 的 roleForEvent：显式指定 > 超管名单 > 平台角色 > everyone。
// 这里不再单独实现一份，避免出现两处判定彼此漂移。

// policyState 持有当前生效的权限表，支持原子替换（F-24 的"权限表"观察项）。
//
// 与限速同理：中间件与提示词提供者在启动时注册一次，之后无法替换，
// 因此让它们每次解引用当前表。换表即换缓存——F-54 要求的"热加载后整体失效"
// 由"换了一个新对象"天然满足，不必再手动 Invalidate。
type policyState struct {
	cur atomic.Pointer[policy.Policy]
}

func newPolicyState(p *policy.Policy) *policyState {
	s := &policyState{}
	s.Store(p)
	return s
}

func (s *policyState) load() *policy.Policy {
	if s == nil {
		return nil
	}
	return s.cur.Load()
}

func (s *policyState) Store(p *policy.Policy) {
	if s == nil || p == nil {
		return
	}
	s.cur.Store(p)
}

// watchPolicyFile 监听权限表文件并热替换（F-24）。
//
// 只在文件存在时启动：默认配置指向的 actions.yaml 常常不存在（用内置基线），
// 监听一个不存在的路径只会持续重试并刷告警。
func watchPolicyFile(ctx context.Context, path string, state *policyState, lg *observe.Logger) *reload.Watcher[*policy.Policy] {
	path = strings.TrimSpace(path)
	if path == "" || state == nil {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	plog := lg.Component("policy")
	var w *reload.Watcher[*policy.Policy]
	w = reload.New([]string{path}, func() (*policy.Policy, error) {
		return policy.LoadFile(path)
	}, reload.Options{
		OnSwap: func(version uint64, _ string) {
			fresh, ok := w.Current()
			if !ok {
				return
			}
			state.Store(fresh)
			// 权限表变化会改变半静态段（提示词里的权限表）与执行侧判定，属预期失效。
			plog.Info("policy table reloaded; the half-static prompt segment changes from the next request",
				"version", version, "path", path)
		},
		Warn: func(err error) {
			plog.Warn("policy reload failed; keeping the previous table", "error", err)
		},
	})
	w.Start(ctx)
	return w
}

// callerFunc 把函数适配成 transport.Caller。
type callerFunc func(context.Context, transport.Request) (transport.Response, error)

func (f callerFunc) Call(ctx context.Context, req transport.Request) (transport.Response, error) {
	return f(ctx, req)
}

// policyMiddleware 按当前角色拦截平台 API 调用（F-53 的执行侧硬拦截）。
//
// fail-closed：ctx 里没有角色时按 everyone 判定；该角色的允许集合里没有这个 action 就拒绝。
// 这里**返回错误**而不是静默丢弃——调用方需要知道"这个动作根本没发出去"，
// 否则会表现成"撤回了但没撤掉"这类难查的现象。
func policyMiddleware(state *policyState, lg *observe.Logger) transport.Middleware {
	return func(next transport.Caller) transport.Caller {
		return callerFunc(func(ctx context.Context, req transport.Request) (transport.Response, error) {
			p := state.load()
			if p == nil {
				return next.Call(ctx, req)
			}
			role := policy.RoleFrom(ctx)
			if role == "" {
				role = policy.RoleEveryone
			}
			if !p.Allow(role, req.Action) {
				if lg != nil {
					lg.Component("policy").Warn("action denied by policy", "role", role, "action", req.Action)
				}
				return transport.Response{}, fmt.Errorf("权限不足：角色 %s 不允许调用 %s", role, req.Action)
			}
			return next.Call(ctx, req)
		})
	}
}

// policyPromptProvider 把权限表渲染叠加到半静态段提供者上（F-53 的提示词侧）。
//
// 放在**半静态段**而不是静态段：角色随会话/用户变化，塞进静态段会让前缀按角色分裂，
// 那正是 F-65 要避免的；表的顺序由 Policy 内部排序保证稳定，只在角色或权限表变化时改变。
func policyPromptProvider(base func(context.Context, session.Key) string, state *policyState, lg *observe.Logger) func(context.Context, session.Key) string {
	return func(ctx context.Context, key session.Key) string {
		text := ""
		if base != nil {
			text = strings.TrimSpace(base(ctx, key))
		}
		p := state.load()
		if p == nil {
			return text
		}
		role := policy.RoleFrom(ctx)
		if role == "" {
			role = policy.RoleEveryone
		}
		table, err := p.Render(role)
		if err != nil {
			// 未知角色：fail-closed 体现在硬拦截上，提示词这里只降级为"只有人格段"。
			if lg != nil {
				lg.Component("policy").Warn("cannot render policy table", "error", err, "role", role)
			}
			return text
		}
		table = strings.TrimSpace(table)
		if text == "" {
			return table
		}
		return text + "\n\n" + table
	}
}
