package moderation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 管理命令相关哨兵错误。
var (
	// ErrForbidden 表示调用者无管理权限。
	ErrForbidden = errors.New("moderation: 调用者无权限")
	// ErrBadArgs 表示命令参数缺失或非法。
	ErrBadArgs = errors.New("moderation: 命令参数错误")
	// ErrBadDuration 表示时长格式非法。
	ErrBadDuration = errors.New("moderation: 时长格式错误")
)

// Authorizer 判定调用者是否为超管（F-14 的 SuperUser）。
type Authorizer interface {
	IsSuperUser(userID int64) bool
}

// AuthorizerFunc 让普通函数满足 Authorizer；nil 函数恒返回 false（fail-closed）。
type AuthorizerFunc func(userID int64) bool

// IsSuperUser 实现 Authorizer。
func (f AuthorizerFunc) IsSuperUser(userID int64) bool {
	if f == nil {
		return false
	}
	return f(userID)
}

// CommanderOptions 是 Commander 的构造参数。
type CommanderOptions struct {
	// MaxList 是 /banlist 返回的最大条数，<=0 时取 100。
	MaxList int
	// Now 提供时间源，nil 表示 time.Now。
	Now func() time.Time
	// Warn 接收非致命告警，可为 nil。
	Warn func(string)
}

// Commander 实现 /ban、/unban、/banlist 管理命令。
//
// 命令在执行前必须通过 Authorizer 权限校验；被封禁对象若是机器人自身或超管，
// Blacklist 会以 ErrProtected 拒绝（白名单优先）。
type Commander struct {
	bans    *Blacklist
	auth    Authorizer
	now     func() time.Time
	warn    func(string)
	maxList int
}

// NewCommander 构造 Commander。
func NewCommander(bans *Blacklist, auth Authorizer, opts CommanderOptions) *Commander {
	maxList := opts.MaxList
	if maxList <= 0 {
		maxList = 100
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Commander{bans: bans, auth: auth, now: now, warn: opts.Warn, maxList: maxList}
}

// CommandResult 是一次命令处理结果。
type CommandResult struct {
	// Handled 表示文本是否被识别为本模块的命令。
	Handled bool
	// Reply 是应回复给调用者的话术（可为空）。
	Reply string
}

// Handle 解析并执行一条管理命令。非本模块命令返回 Handled=false、nil error。
//
// 未授权返回 ErrForbidden、参数错误返回 ErrBadArgs/ErrBadDuration、
// 目标受保护返回 ErrProtected；对应 Reply 中已给出可直接回给用户的话术。
func (c *Commander) Handle(text string, meta Meta) (CommandResult, error) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return CommandResult{Handled: false}, nil
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	switch name {
	case "ban", "unban", "banlist":
	default:
		return CommandResult{Handled: false}, nil
	}
	if !c.authorized(meta) {
		return CommandResult{Handled: true, Reply: "权限不足，该命令仅限超管使用。"}, ErrForbidden
	}
	switch name {
	case "ban":
		return c.handleBan(fields[1:])
	case "unban":
		return c.handleUnban(fields[1:])
	case "banlist":
		return c.handleList()
	default:
		return CommandResult{Handled: false}, nil
	}
}

// authorized 报告调用者是否有权执行管理命令。
func (c *Commander) authorized(meta Meta) bool {
	return c.auth != nil && c.auth.IsSuperUser(meta.UserID)
}

// handleBan 处理 /ban [kind] <id> [duration]。
func (c *Commander) handleBan(args []string) (CommandResult, error) {
	kind, id, rest, err := parseSubject(args)
	if err != nil {
		return CommandResult{Handled: true, Reply: "用法：/ban [user|group|ip] <id> [duration]"}, err
	}
	var ttl time.Duration
	if len(rest) > 0 {
		ttl, err = parseTTL(rest[0])
		if err != nil {
			return CommandResult{Handled: true, Reply: "时长格式非法，示例：60s、10m、1h、1d"}, err
		}
	}
	if err := c.bans.Ban(kind, id, "admin ban", ttl); err != nil {
		c.warnf("moderation: /ban %s %s 失败: %v", kind, id, err)
		reply := "封禁失败：" + err.Error()
		if errors.Is(err, ErrProtected) {
			reply = "目标在保护名单内（机器人自身或超管），拒绝封禁。"
		}
		return CommandResult{Handled: true, Reply: reply}, err
	}
	scope := "永久"
	if ttl > 0 {
		scope = ttl.String()
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf("已封禁 %s %s（%s）。", kind, id, scope)}, nil
}

// handleUnban 处理 /unban [kind] <id>。
func (c *Commander) handleUnban(args []string) (CommandResult, error) {
	kind, id, _, err := parseSubject(args)
	if err != nil {
		return CommandResult{Handled: true, Reply: "用法：/unban [user|group|ip] <id>"}, err
	}
	existed, err := c.bans.Unban(kind, id)
	if err != nil {
		return CommandResult{Handled: true, Reply: "解封失败：" + err.Error()}, err
	}
	if !existed {
		return CommandResult{Handled: true, Reply: fmt.Sprintf("%s %s 本就不在黑名单中。", kind, id)}, nil
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf("已解封 %s %s。", kind, id)}, nil
}

// handleList 处理 /banlist。
func (c *Commander) handleList() (CommandResult, error) {
	entries := c.bans.List()
	if len(entries) == 0 {
		return CommandResult{Handled: true, Reply: "黑名单为空。"}, nil
	}
	now := c.now()
	var b strings.Builder
	b.WriteString("黑名单：\n")
	for i, e := range entries {
		if i >= c.maxList {
			fmt.Fprintf(&b, "…… 其余 %d 条已省略\n", len(entries)-c.maxList)
			break
		}
		expires := "永久"
		if !e.ExpiresAt.IsZero() {
			remain := e.ExpiresAt.Sub(now)
			if remain < 0 {
				remain = 0
			}
			expires = "剩余 " + remain.Truncate(time.Second).String()
		}
		fmt.Fprintf(&b, "- %s %s（%s）%s\n", e.Kind, e.ID, expires, e.Reason)
	}
	return CommandResult{Handled: true, Reply: strings.TrimRight(b.String(), "\n")}, nil
}

// warnf 在注入告警回调时记录一条告警。
func (c *Commander) warnf(format string, args ...any) {
	if c.warn == nil {
		return
	}
	c.warn(fmt.Sprintf(format, args...))
}

// parseSubject 解析可选的维度前缀与目标 ID，返回剩余参数。
func parseSubject(args []string) (BanKind, string, []string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return BanUser, "", nil, ErrBadArgs
	}
	kind := BanUser
	idx := 0
	if k, ok := ParseBanKind(strings.ToLower(args[0])); ok {
		kind = k
		idx++
	}
	if idx >= len(args) || strings.TrimSpace(args[idx]) == "" {
		return BanUser, "", nil, ErrBadArgs
	}
	id := args[idx]
	if kind != BanIP {
		if _, err := parseID(id); err != nil {
			return BanUser, "", nil, fmt.Errorf("%w: %w", ErrBadArgs, err)
		}
	}
	return kind, id, args[idx+1:], nil
}

// parseTTL 解析时长：支持 time.ParseDuration 的写法与 "Nd"（天）；空串表示永久。
func parseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, "d"), 10, 64)
		if err != nil || n <= 0 {
			return 0, ErrBadDuration
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, ErrBadDuration
	}
	return d, nil
}
