package router

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// MemberLookup 提供群成员角色查询；实现方必须尊重 ctx 超时。
//
// **注意：当前没有生产调用方。** 它只服务于下面的 GroupAdmin / GroupOwner /
// HigherThan 三条规则，而组合根一条都没注册：管理命令的鉴权走
// admin.Module 自己的 Checker（角色来自 access.roleForEvent，见 cmd/server/access.go）。
// 与 policy.MemberLookup 是同一份签名的第二份声明，但两者**分别**服务于
// 两个各自未接线的库，所以合并它们没有意义（见 HANDOFF「已知偏离」）。
type MemberLookup interface {
	MemberRole(ctx context.Context, groupID, userID int64) (string, error)
}

// 角色等级：owner > admin > member。
func roleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member":
		return 1
	default:
		return 0
	}
}

// Kind 匹配事件大类/细分；"message/group" 用 "/" 分隔。
func Kind(kind string) Rule {
	return func(c *Ctx) bool { return KindMatches(kind, c.Event) }
}

// Command 匹配命令前缀并解析参数。
//
// 命中时写入 StateKeyCommand（命令名）与 StateKeyArgs（[]string）。
func Command(prefix string, cmds ...string) Rule {
	want := make(map[string]struct{}, len(cmds))
	for _, c := range cmds {
		want[c] = struct{}{}
	}
	return func(c *Ctx) bool {
		text := strings.TrimSpace(c.MessageString())
		if text == "" {
			return false
		}
		body := text
		if prefix != "" {
			if !strings.HasPrefix(body, prefix) {
				return false
			}
			body = strings.TrimPrefix(body, prefix)
		}
		fields := strings.Fields(body)
		if len(fields) == 0 {
			return false
		}
		name := fields[0]
		if len(want) > 0 {
			if _, ok := want[name]; !ok {
				return false
			}
		}
		rest := strings.TrimSpace(strings.TrimPrefix(body, name))
		args, err := ParseCommandArgs(rest)
		if err != nil {
			return false
		}
		c.Set(StateKeyCommand, name)
		c.Set(StateKeyArgs, args)
		return true
	}
}

// Prefix 匹配纯文本前缀。
func Prefix(ps ...string) Rule {
	return func(c *Ctx) bool {
		text := c.MessageString()
		for _, p := range ps {
			if p != "" && strings.HasPrefix(text, p) {
				return true
			}
		}
		return false
	}
}

// Suffix 匹配纯文本后缀。
func Suffix(ss ...string) Rule {
	return func(c *Ctx) bool {
		text := c.MessageString()
		for _, s := range ss {
			if s != "" && strings.HasSuffix(text, s) {
				return true
			}
		}
		return false
	}
}

// Keyword 匹配是否包含任一关键词。
func Keyword(ks ...string) Rule {
	return func(c *Ctx) bool {
		text := c.MessageString()
		for _, k := range ks {
			if k != "" && strings.Contains(text, k) {
				return true
			}
		}
		return false
	}
}

// FullMatch 匹配纯文本全等。
func FullMatch(ss ...string) Rule {
	return func(c *Ctx) bool {
		text := c.MessageString()
		for _, s := range ss {
			if text == s {
				return true
			}
		}
		return false
	}
}

// Regexp 预编译正则并把子匹配写入 StateKeyRegexMatch。
func Regexp(pattern string) (Rule, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return func(c *Ctx) bool {
		m := re.FindStringSubmatch(c.MessageString())
		if m == nil {
			return false
		}
		c.Set(StateKeyRegexMatch, m)
		return true
	}, nil
}

// Regex 是 Regexp 的便捷版本：模式非法时返回一条永不匹配的规则。
//
// 需要拿到编译错误时请用 Regexp。
func Regex(pattern string) Rule {
	r, err := Regexp(pattern)
	if err != nil {
		return func(*Ctx) bool { return false }
	}
	return r
}

// AtMe 判断消息是否 @ 了机器人。
func AtMe() Rule {
	return func(c *Ctx) bool {
		ev := c.Event
		if ev == nil {
			return false
		}
		self := strconv.FormatInt(ev.SelfID, 10)
		for _, seg := range ev.Message {
			if seg.Type == event.TypeAt && seg.Data["qq"] == self {
				return true
			}
		}
		if v, ok := ev.Get("to_me"); ok {
			switch t := v.(type) {
			case bool:
				return t
			case string:
				return t == "true"
			}
		}
		return false
	}
}

// OnlyGroup 只在群消息上通过。
func OnlyGroup() Rule {
	return func(c *Ctx) bool { return c.Event != nil && c.Event.Sub == "group" }
}

// OnlyPrivate 只在私聊消息上通过。
func OnlyPrivate() Rule {
	return func(c *Ctx) bool { return c.Event != nil && c.Event.Sub == "private" }
}

// OnlyToMe 只在"对我说"的场景通过（私聊，或群里 @ 我）。
func OnlyToMe() Rule {
	atMe := AtMe()
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		if c.Event.Sub == "private" {
			return true
		}
		return atMe(c)
	}
}

// SuperUser 匹配配置的超管列表。
func SuperUser(ids ...int64) Rule {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		_, ok := set[c.Event.UserID]
		return ok
	}
}

// CheckUser 匹配指定用户。
func CheckUser(ids ...int64) Rule {
	return SuperUser(ids...)
}

// CheckGroup 匹配指定群。
func CheckGroup(ids ...int64) Rule {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		_, ok := set[c.Event.GroupID]
		return ok
	}
}

func lookupRole(c *Ctx, lookup MemberLookup) (string, bool) {
	if c.Event == nil || lookup == nil {
		return "", false
	}
	ctx := context.Context(c)
	role, err := lookup.MemberRole(ctx, c.Event.GroupID, c.Event.UserID)
	if err != nil {
		// fail-closed：查询失败一律当作无权限。
		return "", false
	}
	return role, true
}

// ---- 下面三条角色规则当前都没有生产调用方 ----
//
// 它们要求宿主提供一个 MemberLookup（按 QQ 查群成员角色）。组合根没有注册任何一条：
// 管理命令的授权在 admin.Module 里完成，角色由 access.roleForEvent 从事件里解析。
// 保留它们是为了可复用内核的完整性；改动不会影响线上行为。

// GroupAdmin 要求群管理员或群主；查询失败时返回 false（fail-closed）。
func GroupAdmin(lookup MemberLookup) Rule {
	return func(c *Ctx) bool {
		role, ok := lookupRole(c, lookup)
		return ok && roleRank(role) >= roleRank("admin")
	}
}

// GroupOwner 要求群主。
func GroupOwner(lookup MemberLookup) Rule {
	return func(c *Ctx) bool {
		role, ok := lookupRole(c, lookup)
		return ok && roleRank(role) >= roleRank("owner")
	}
}

// HigherThan 要求发起者等级高于 target 返回的用户。
func HigherThan(lookup MemberLookup, target func(*Ctx) int64) Rule {
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		other := target(c)
		if other == c.Event.UserID {
			return false
		}
		mine, ok := lookupRole(c, lookup)
		if !ok {
			return false
		}
		ctx := context.Context(c)
		theirs, err := lookup.MemberRole(ctx, c.Event.GroupID, other)
		if err != nil {
			return false
		}
		return roleRank(mine) > roleRank(theirs)
	}
}

// HasImage 命中含图片的消息，并把图片 file 写入 StateKeyImageURLs。
func HasImage() Rule {
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		var urls []string
		for _, seg := range c.Event.Message {
			if seg.Type == event.TypeImage {
				urls = append(urls, seg.Data["url"])
				if seg.Data["url"] == "" {
					urls[len(urls)-1] = seg.Data["file"]
				}
			}
		}
		if len(urls) == 0 {
			return false
		}
		c.Set(StateKeyImageURLs, urls)
		return true
	}
}

// HasReply 命中回复消息，并把被回复的消息 ID 写入 StateKeyReplyID。
func HasReply() Rule {
	return func(c *Ctx) bool {
		if c.Event == nil {
			return false
		}
		for _, seg := range c.Event.Message {
			if seg.Type == event.TypeReply && seg.Data["id"] != "" {
				c.Set(StateKeyReplyID, seg.Data["id"])
				return true
			}
		}
		return false
	}
}

// Never 是一条永不匹配的规则（测试与占位用）。
func Never() Rule { return func(*Ctx) bool { return false } }

// Always 是一条恒真规则。
func Always() Rule { return func(*Ctx) bool { return true } }
