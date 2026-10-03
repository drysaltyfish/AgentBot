package scoped

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/drysaltyfish/agentbot/internal/session"
)

// SessionRef 唯一标识一个会话的人格归属：作用域 + 用户。
type SessionRef struct {
	// Scope 是会话作用域路径（见 GroupScope / UserScope / SessionScopes）。
	Scope string
	// User 是用户标识；同一作用域下可再按用户区分。
	User string
}

// String 返回稳定的持久化键，用长度前缀分隔字段避免歧义。
func (r SessionRef) String() string {
	scope := CleanScope(r.Scope)
	user := strings.TrimSpace(r.User)
	return fmt.Sprintf("v1:%d:%s:%d:%s", len(scope), scope, len(user), user)
}

// RouteScope 返回参与路由键的作用域串（作用域 + 用户）。
func (r SessionRef) RouteScope() string {
	return ScopePath(CleanScope(r.Scope), r.User)
}

// SessionRefForKey 把会话键映射为人格归属（F-82）。
//
// 粒度与记忆一致：群内按用户、私聊就是用户本人。SelfID 不进入作用域——
// 它属于账号维度（F-07，本次明确不做），不是人格维度；多账号接入时
// 这里必须重新审视（同一群号的两个人格会共用持久化键）。
func SessionRefForKey(key session.Key) SessionRef {
	scope := ""
	if key.GroupID != 0 {
		scope = GroupScope(strconv.FormatInt(key.GroupID, 10))
	}
	return SessionRef{Scope: scope, User: strconv.FormatInt(key.UserID, 10)}
}

// PersonaStore 持久化 SessionRef → persona 的映射。
//
// 本包不碰任何数据库：SQLite / JSONL 实现由调用方注入（F-82 明确要求）。
type PersonaStore interface {
	// Persona 返回该会话已持久化的人格；ok=false 表示从未设置。
	//
	// 带 ctx：读的是数据库，必须能被请求的取消/超时截断。
	Persona(ctx context.Context, ref SessionRef) (string, bool, error)
	// SetPersona 写入该会话的人格。
	SetPersona(ctx context.Context, ref SessionRef, persona string) error
}

// MemoryStore 是进程内 PersonaStore 实现（测试与无持久层时使用）。
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewMemoryStore 构造空的进程内人格映射。
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]string{}} }

// Persona 返回该会话已持久化的人格。
func (s *MemoryStore) Persona(_ context.Context, ref SessionRef) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.m[ref.String()]
	return p, ok, nil
}

// SetPersona 写入该会话的人格。
func (s *MemoryStore) SetPersona(_ context.Context, ref SessionRef, persona string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[ref.String()] = NormalizePersona(persona)
	return nil
}

// Manager 把分层配置、人格注册表与持久化映射接在一起。
type Manager struct {
	cfg   *Config
	reg   *Registry
	store PersonaStore
	log   *slog.Logger
}

// NewManager 构造 Manager；cfg 与 reg 必填，store 与 log 可为 nil。
func NewManager(cfg *Config, reg *Registry, store PersonaStore, log *slog.Logger) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scoped: 配置为空")
	}
	if reg == nil {
		return nil, fmt.Errorf("scoped: 人格注册表为空")
	}
	return &Manager{cfg: cfg, reg: reg, store: store, log: log}, nil
}

// Persona 解析会话当前人格：持久化映射 → 配置键 persona → DefaultPersona。
//
// 空人格归一为 DefaultPersona，因此键始终稳定。已持久化但已不存在的人格
// 会告警并回退，而不是让会话永久不可用。
func (m *Manager) Persona(ctx context.Context, ref SessionRef) (string, error) {
	if m.store != nil {
		p, ok, err := m.store.Persona(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("scoped: 读取会话人格: %w", err)
		}
		if ok {
			if p = strings.TrimSpace(p); p != "" {
				if m.reg.Has(p) {
					return p, nil
				}
				m.warn("已持久化的人格不存在，回退默认", "persona", p, "scope", ref.Scope, "user", ref.User)
			}
		}
	}
	if v, ok := m.cfg.Get(ref.Scope, KeyPersona); ok {
		if name, isStr := v.(string); isStr {
			if name = strings.TrimSpace(name); name != "" {
				if m.reg.Has(name) {
					return name, nil
				}
				m.warn("配置引用的人格不存在，回退默认", "persona", name, "scope", ref.Scope)
			}
		}
	}
	return DefaultPersona, nil
}

// SetPersona 切换会话人格并持久化；返回人格是否真的发生变化。
//
// 未注入 PersonaStore 时拒绝——静默不落盘会让"切换"在重启后消失。
// 人格变化会让 F-65 半静态段哈希改变，因此这里记一条日志（预期失效）。
func (m *Manager) SetPersona(ctx context.Context, ref SessionRef, persona string) (bool, error) {
	name := NormalizePersona(persona)
	if !ValidPersonaName(name) {
		return false, fmt.Errorf("scoped: 非法人格名 %q", persona)
	}
	if !m.reg.Has(name) {
		return false, fmt.Errorf("scoped: 人格 %q 未定义", name)
	}
	current, err := m.Persona(ctx, ref)
	if err != nil {
		return false, err
	}
	if m.store == nil {
		return false, fmt.Errorf("scoped: 未注入 PersonaStore，无法持久化人格")
	}
	if err := m.store.SetPersona(ctx, ref, name); err != nil {
		return false, fmt.Errorf("scoped: 持久化会话人格: %w", err)
	}
	changed := current != name
	if changed && m.log != nil {
		m.log.Info("人格已切换，F-65 半静态段哈希将变化",
			"scope", ref.Scope, "user", ref.User, "from", current, "to", name,
			"route_key", RouteKey(ref.RouteScope(), name))
	}
	return changed, nil
}

// RouteKey 返回会话当前人格的路由/缓存键。
func (m *Manager) RouteKey(ctx context.Context, ref SessionRef) (string, error) {
	p, err := m.Persona(ctx, ref)
	if err != nil {
		return "", err
	}
	return RouteKey(ref.RouteScope(), p), nil
}

// Fingerprint 返回当前人格设定的短摘要，供 F-65 半静态段哈希使用。
func (m *Manager) Fingerprint(ctx context.Context, ref SessionRef) (string, error) {
	p, err := m.Persona(ctx, ref)
	if err != nil {
		return "", err
	}
	if persona, ok := m.reg.Get(p); ok {
		return persona.Fingerprint(), nil
	}
	return Persona{Name: p}.Fingerprint(), nil
}

func (m *Manager) warn(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}
