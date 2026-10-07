// Package policy 实现"权限即提示词"与权限判定缓存（FEATURES.md F-53 / F-54）。
//
// 双层防护：渲染进系统提示词做输入侧约束，Policy.Allow 做执行侧硬拦截。
package policy

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	// ErrUnknownRole 表示角色不在权限表里（fail-closed，不返回空表）。
	ErrUnknownRole = errors.New("unknown role")
	// ErrUndefinedAction 表示权限表引用了未定义的 action。
	ErrUndefinedAction = errors.New("role references undefined action")
	// ErrNoActions 表示权限表里没有任何 action。
	ErrNoActions = errors.New("policy has no actions")
)

//go:embed actions.yaml
var defaultTable []byte

// MandatorySentence 是必须出现在提示词里的那句话。
const MandatorySentence = "列表中没有的 action 不允许调用。"

// ActionSpec 描述一个动作。
type ActionSpec struct {
	Desc   string `yaml:"desc"`
	Params string `yaml:"params"`
	Data   string `yaml:"data"`
}

// Table 是权限表的原始形态。
type Table struct {
	Actions map[string]ActionSpec `yaml:"actions"`
	Roles   map[string][]string   `yaml:"config"`
}

// Policy 是校验后的权限表 + 判定缓存。
type Policy struct {
	table   Table
	ordered []string

	mu    sync.RWMutex
	cache map[string]map[string]struct{}
	fills int
}

// Load 解析权限表并校验（引用未定义 action 直接报错）。
func Load(data []byte) (*Policy, error) {
	var t Table
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	p := &Policy{table: t, cache: map[string]map[string]struct{}{}}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadDefault 加载内置默认权限表。
func LoadDefault() (*Policy, error) { return Load(defaultTable) }

// LoadFile 从外部文件加载；失败时保留调用方自行处理的余地。
func LoadFile(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy %s: %w", path, err)
	}
	return Load(raw)
}

// Validate 收集全部问题后一次性返回。
func (p *Policy) Validate() error {
	if len(p.table.Actions) == 0 {
		return ErrNoActions
	}
	names := make([]string, 0, len(p.table.Actions))
	for name := range p.table.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	p.ordered = names

	var problems []string
	roles := make([]string, 0, len(p.table.Roles))
	for role := range p.table.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		for _, action := range p.table.Roles[role] {
			if _, ok := p.table.Actions[action]; !ok {
				problems = append(problems, fmt.Sprintf("role %q references undefined action %q", role, action))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrUndefinedAction, strings.Join(problems, "; "))
	}
	return nil
}

// Roles 返回全部角色名（有序）。
func (p *Policy) Roles() []string {
	roles := make([]string, 0, len(p.table.Roles))
	for role := range p.table.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// Actions 返回全部 action 名（有序，保证渲染稳定）。
func (p *Policy) Actions() []string {
	out := make([]string, len(p.ordered))
	copy(out, p.ordered)
	return out
}

// cachedSet 首次访问某角色时把切片折叠成 set（F-54：未命中时填充）。
func (p *Policy) cachedSet(role string) (map[string]struct{}, bool) {
	p.mu.RLock()
	set, ok := p.cache[role]
	p.mu.RUnlock()
	if ok {
		return set, true
	}
	list, exists := p.table.Roles[role]
	if !exists {
		return nil, false
	}
	set = make(map[string]struct{}, len(list))
	for _, a := range list {
		set[a] = struct{}{}
	}
	p.mu.Lock()
	p.cache[role] = set
	p.fills++
	p.mu.Unlock()
	return set, true
}

// ReadOnlyActions 是**任何角色都可调用**的只读动作白名单。
//
// 为什么需要它：权限表的语义是"模型能对平台做哪些**改变**"（撤消息、禁言…），
// 而查询类动作不改变任何状态，把"查一下这个人是谁"也纳入授权，只会让工具
// 在角色缺失时无声失败（真实故障：get_user_info 因 everyone 不含
// get_group_member_info 而报错），收益却是零——没有一种威胁模型需要
// "允许他发消息，但禁止他知道发消息的人叫什么"。
//
// 注意它只豁免**读**：写动作一律照旧走角色判定。
var ReadOnlyActions = map[string]struct{}{
	"get_group_member_info": {},
	"get_group_member_list": {},
	"get_login_info":        {},
	"get_stranger_info":     {},
	"get_friend_list":       {},
	"get_group_list":        {},
	"get_group_info":        {},
}

// IsReadOnly 报告某个 action 是否属于只读查询。
func IsReadOnly(action string) bool {
	_, ok := ReadOnlyActions[action]
	return ok
}

// Allow 判定某角色能否调用某 action；未知角色 fail-closed 返回 false。
//
// 只读动作不受角色限制（见 ReadOnlyActions）。
func (p *Policy) Allow(role, action string) bool {
	if IsReadOnly(action) {
		return true
	}
	set, ok := p.cachedSet(role)
	if !ok {
		return false
	}
	_, allowed := set[action]
	return allowed
}

// CacheFills 返回缓存填充次数（测试用）。
func (p *Policy) CacheFills() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fills
}

// Invalidate 整体失效缓存（热加载后必须调用，F-54 边界）。
func (p *Policy) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = map[string]map[string]struct{}{}
}

// Render 把某角色允许的 action 渲染成 Markdown 表格，并附上强制声明。
func (p *Policy) Render(role string) (string, error) {
	set, ok := p.cachedSet(role)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	var b strings.Builder
	b.WriteString("| 功能 | action | params | data |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, name := range p.ordered {
		if _, allowed := set[name]; !allowed {
			continue
		}
		spec := p.table.Actions[name]
		b.WriteString("| " + spec.Desc + " | " + name + " | " + spec.Params + " | " + spec.Data + " |\n")
	}
	b.WriteString("\n" + MandatorySentence + "\n")
	return b.String(), nil
}

// MemberLookup 提供群成员角色查询（由调用方实现，必须尊重 ctx 超时）。
//
// **注意：当前没有生产调用方。** 它只服务于下面的 RoleResolver，而组合根
// 刻意没有采用那条路径——角色由 access.roleForEvent 解析（显式 access.roles >
// 超管名单 > **事件里平台已上报的** Sender.Role > everyone），不需要为每条消息
// 再查一次平台 API。见 cmd/server/policy.go 的说明与 HANDOFF「已知偏离」。
//
// 保留它是为了可复用内核的完整性；改它不会影响线上行为。真要用它接线，
// 先想清楚"事件里已经有角色了，为什么还要多一次查询"。
type MemberLookup interface {
	MemberRole(ctx context.Context, groupID, userID int64) (string, error)
}

// RoleResolver 在库内把"谁在说话"推导成角色名。
//
// 不接受宿主传入的裸角色字符串：宿主传错即可提权（参考实现踩过的坑）。
//
// **注意：当前没有生产调用方**（与 MemberLookup 同理）。线上用的是
// access.roleForEvent + policy.WithRole/ RoleFrom：角色在事件入口解析一次、
// 放进 ctx，出口只从 ctx 读——不再经过这里，也不再查平台 API。
type RoleResolver struct {
	SuperUsers map[int64]struct{}
	Lookup     MemberLookup
}

// NewRoleResolver 构造解析器。
func NewRoleResolver(superUsers []int64, lookup MemberLookup) *RoleResolver {
	set := make(map[int64]struct{}, len(superUsers))
	for _, id := range superUsers {
		set[id] = struct{}{}
	}
	return &RoleResolver{SuperUsers: set, Lookup: lookup}
}

// Resolve 推导角色；任何不确定都落到 everyone（fail-closed）。
func (r *RoleResolver) Resolve(ctx context.Context, groupID, userID int64) string {
	if r == nil {
		return RoleEveryone
	}
	if _, ok := r.SuperUsers[userID]; ok {
		return RoleSuperUser
	}
	if r.Lookup == nil || groupID == 0 {
		return RoleEveryone
	}
	role, err := r.Lookup.MemberRole(ctx, groupID, userID)
	if err != nil {
		return RoleEveryone
	}
	switch role {
	case RoleOwner, RoleAdmin:
		return role
	case "member":
		return RoleMember
	default:
		return RoleEveryone
	}
}

// 角色常量。
const (
	RoleSuperUser = "superuser"
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleMember    = "member"
	RoleEveryone  = "everyone"
)
