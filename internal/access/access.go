// Package access 实现两件"谁能用这个机器人"的判定（F-58 的运维扩展）：
//
//  1. 名单：哪些用户/群的消息**应当在路由层就被丢弃**（不进入任何后续处理）；
//  2. 角色：用 QQ 号**直接指定**角色，覆盖平台上报的群成员角色。
//
// 两者都不碰消息内容，只做"放行 / 丢弃"与"是谁"的判定，因此不依赖 event、
// router 之外的任何东西，可单独测试。
package access

import (
	"sort"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/policy"
)

// Mode 是名单模式。
type Mode string

const (
	// ModeOff 不启用名单（全部放行）。
	ModeOff Mode = "off"
	// ModeAllow 白名单：只有名单内放行，其余丢弃。
	ModeAllow Mode = "allow"
	// ModeDeny 黑名单：名单内丢弃，其余放行。
	ModeDeny Mode = "deny"
)

// NormalizeMode 把配置里的字符串归一成 Mode；空串与未知值都落到 off（由配置校验负责拦未知值）。
func NormalizeMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeAllow:
		return ModeAllow
	case ModeDeny:
		return ModeDeny
	default:
		return ModeOff
	}
}

// Listed 复刻名单的原始输入：用户号与群号。
type Listed struct {
	Users  []int64
	Groups []int64
}

// Policy 是名单判定。
//
// 语义（与 README 的表述一一对应）：
//   - 私聊：只看用户名单；
//   - 群聊：群名单决定整个群；CheckUsersInGroup 为 true 时用户名单**也在群里生效**
//     （allow 模式要求群与人都命中；deny 模式任一名单命中即丢弃）。
type Policy struct {
	mode              Mode
	users             map[int64]struct{}
	groups            map[int64]struct{}
	checkUsersInGroup bool
}

// NewPolicy 构造名单判定；mode 为 off（或未知）时 Active() 为 false。
func NewPolicy(mode string, listed Listed, checkUsersInGroup bool) *Policy {
	p := &Policy{
		mode:              NormalizeMode(mode),
		users:             toSet(listed.Users),
		groups:            toSet(listed.Groups),
		checkUsersInGroup: checkUsersInGroup,
	}
	return p
}

// Active 报告名单是否生效。
func (p *Policy) Active() bool { return p != nil && p.mode != ModeOff }

// Mode 返回归一后的模式。
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeOff
	}
	return p.mode
}

// Allow 判定这次事件是否应当被处理。
//
// 参数是原始 QQ 号与群号；groupID 为 0 表示私聊。
func (p *Policy) Allow(userID, groupID int64) bool {
	if !p.Active() {
		return true
	}
	userListed := has(p.users, userID)
	if groupID == 0 {
		return p.decide(userListed)
	}
	groupListed := has(p.groups, groupID)

	if p.mode == ModeAllow {
		if p.checkUsersInGroup {
			// 群与人都要在名单里：适合"只服务指定群里的指定人"。
			return groupListed && userListed
		}
		return groupListed || userListed
	}
	// deny：群在名单里整群丢弃；否则仅当"人名单也在群里生效"时才按人丢。
	if groupListed {
		return false
	}
	if p.checkUsersInGroup && userListed {
		return false
	}
	return true
}

func (p *Policy) decide(userListed bool) bool {
	if p.mode == ModeAllow {
		return userListed
	}
	return !userListed
}

// Summary 返回可读的模式描述，用于启动日志。
func (p *Policy) Summary() string {
	if !p.Active() {
		return "off"
	}
	return string(p.mode) + " users=" + joinIDs(p.users) + " groups=" + joinIDs(p.groups)
}

// Roles 是"QQ 号 → 角色"的显式指定。
//
// 它优先于平台上报的群成员角色：平台的 owner/admin 是"群里的身份"，
// 而这里表达的是"部署者认定他是谁"——两者冲突时以部署者的显式指定为准。
type Roles struct {
	byUser map[int64]string
}

// NewRoles 按角色名归并 QQ 号；未知角色名直接忽略（由配置校验拦下）。
func NewRoles(groups map[string][]int64) *Roles {
	r := &Roles{byUser: map[int64]string{}}
	for role, ids := range groups {
		name := normalizeRole(role)
		if name == "" {
			continue
		}
		for _, id := range ids {
			if id == 0 {
				continue
			}
			// 冲突时保留权限更高的那个，避免配置顺序决定权限。
			if existing, ok := r.byUser[id]; ok && rank(existing) >= rank(name) {
				continue
			}
			r.byUser[id] = name
		}
	}
	return r
}

// Lookup 返回该 QQ 号被显式指定的角色。
func (r *Roles) Lookup(userID int64) (string, bool) {
	if r == nil {
		return "", false
	}
	role, ok := r.byUser[userID]
	return role, ok
}

// Len 返回被显式指定的用户数（启动日志用）。
func (r *Roles) Len() int {
	if r == nil {
		return 0
	}
	return len(r.byUser)
}

// SuperUsers 返回被指定为超管的 QQ 号（升序）。
func (r *Roles) SuperUsers() []int64 {
	if r == nil {
		return nil
	}
	out := make([]int64, 0, len(r.byUser))
	for id, role := range r.byUser {
		if role == policy.RoleSuperUser {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// normalizeRole 把配置里的角色名归一；不支持的名字返回空串。
func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case policy.RoleSuperUser:
		return policy.RoleSuperUser
	case policy.RoleOwner:
		return policy.RoleOwner
	case policy.RoleAdmin:
		return policy.RoleAdmin
	case policy.RoleMember:
		return policy.RoleMember
	default:
		return ""
	}
}

// rank 给出角色的权限高低，用于解决"同一个 QQ 被写进多个角色"的冲突。
func rank(role string) int {
	switch role {
	case policy.RoleSuperUser:
		return 4
	case policy.RoleOwner:
		return 3
	case policy.RoleAdmin:
		return 2
	case policy.RoleMember:
		return 1
	default:
		return 0
	}
}

func toSet(ids []int64) map[int64]struct{} {
	out := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			out[id] = struct{}{}
		}
	}
	return out
}

func has(set map[int64]struct{}, id int64) bool {
	_, ok := set[id]
	return ok
}

func joinIDs(set map[int64]struct{}) string {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
