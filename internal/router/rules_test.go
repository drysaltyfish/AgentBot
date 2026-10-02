package router

import (
	"context"
	"errors"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
)

const (
	privateMessage = `{"post_type":"message","message_type":"private","sub_type":"friend","self_id":10001,"user_id":20002,"message_id":3,"sender":{"user_id":20002,"role":"member"},"message":[{"type":"text","data":{"text":"hello there"}}]}`
	groupAtMe      = `{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":4,"sender":{"user_id":20002,"role":"member"},"message":[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":" help"}}]}`
	groupImage     = `{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":5,"sender":{"user_id":20002,"role":"member"},"message":[{"type":"image","data":{"url":"https://example.com/a.png"}}]}`
	groupReply     = `{"post_type":"message","message_type":"group","sub_type":"normal","self_id":10001,"user_id":20002,"group_id":30003,"message_id":6,"sender":{"user_id":20002,"role":"member"},"message":[{"type":"reply","data":{"id":"42"}},{"type":"text","data":{"text":"nice"}}]}`
	noticeNoSender = `{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":10001,"user_id":20002,"group_id":30003,"message_id":7}`
)

func ctxFor(t *testing.T, raw string) *Ctx {
	t.Helper()
	return NewCtx(context.Background(), event.NewEvent([]byte(raw)), nil)
}

func Test_F14_TextRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rule Rule
		raw  string
		want bool
	}{
		{"prefix hit", Prefix("/ping"), groupMessage, true},
		{"prefix second alternative", Prefix("/x", "/ping"), groupMessage, true},
		{"prefix miss", Prefix("/pong"), groupMessage, false},
		{"suffix miss", Suffix("!!"), groupMessage, false},
		{"keyword hit", Keyword("hello"), groupMessage, true},
		{"keyword miss", Keyword("nope"), groupMessage, false},
		{"fullmatch miss", FullMatch("/ping hello!"), groupMessage, false},
		{"regex hit", Regex("^/p[a-z]+ "), groupMessage, true},
		{"regex miss", Regex("^!bang"), groupMessage, false},
	}
	for _, tc := range cases {
		if got := tc.rule(ctxFor(t, tc.raw)); got != tc.want {
			t.Fatalf("%s: actual=%v expected=%v", tc.name, got, tc.want)
		}
	}
}

func Test_F14_CommandWritesArgsAsStringSlice(t *testing.T) {
	t.Parallel()
	c := ctxFor(t, groupMessage)
	rule := Command("/", "ping")
	if !rule(c) {
		t.Fatalf("command did not match")
	}
	cmd, ok := c.GetString(StateKeyCommand)
	if !ok || cmd != "ping" {
		t.Fatalf("command: actual=(%q,%v) expected=(ping,true)", cmd, ok)
	}
	args, ok := c.GetStrings(StateKeyArgs)
	if !ok {
		t.Fatalf("args missing or not []string: actual=%v", c.State[StateKeyArgs])
	}
	if len(args) != 1 || args[0] != "hello" {
		t.Fatalf("args: actual=%v expected=[hello]", args)
	}

	if Command("/", "pong")(ctxFor(t, groupMessage)) {
		t.Fatalf("command should not match a different name")
	}
	if Command("/", "ping")(ctxFor(t, privateMessage)) {
		t.Fatalf("command should not match a message without the prefix")
	}
}

func Test_F14_ScopeRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rule Rule
		raw  string
		want bool
	}{
		{"only group on group", OnlyGroup(), groupMessage, true},
		{"only group on private", OnlyGroup(), privateMessage, false},
		{"only group on notice", OnlyGroup(), noticeNoSender, false},
		{"only private on private", OnlyPrivate(), privateMessage, true},
		{"only private on group", OnlyPrivate(), groupMessage, false},
		{"only to me on private", OnlyToMe(), privateMessage, true},
		{"only to me on at-me group", OnlyToMe(), groupAtMe, true},
		{"only to me on plain group", OnlyToMe(), groupMessage, false},
		{"at me on at-me", AtMe(), groupAtMe, true},
		{"at me on plain", AtMe(), groupMessage, false},
		{"at me on notice", AtMe(), noticeNoSender, false},
	}
	for _, tc := range cases {
		if got := tc.rule(ctxFor(t, tc.raw)); got != tc.want {
			t.Fatalf("%s: actual=%v expected=%v", tc.name, got, tc.want)
		}
	}
}

func Test_F14_MediaRules(t *testing.T) {
	t.Parallel()
	c := ctxFor(t, groupImage)
	if !HasImage()(c) {
		t.Fatalf("HasImage did not match an image message")
	}
	urls, ok := c.GetStrings(StateKeyImageURLs)
	if !ok || len(urls) != 1 || urls[0] != "https://example.com/a.png" {
		t.Fatalf("image urls: actual=(%v,%v)", urls, ok)
	}
	if HasImage()(ctxFor(t, groupMessage)) {
		t.Fatalf("HasImage matched a text-only message")
	}

	rc := ctxFor(t, groupReply)
	if !HasReply()(rc) {
		t.Fatalf("HasReply did not match a reply message")
	}
	if id, ok := rc.GetString(StateKeyReplyID); !ok || id != "42" {
		t.Fatalf("reply id: actual=(%q,%v)", id, ok)
	}
	if HasReply()(ctxFor(t, groupMessage)) {
		t.Fatalf("HasReply matched a non-reply message")
	}
}

func Test_F14_IdRules(t *testing.T) {
	t.Parallel()
	if !SuperUser(20002, 1)(ctxFor(t, groupMessage)) {
		t.Fatalf("SuperUser did not match a listed id")
	}
	if SuperUser(999)(ctxFor(t, groupMessage)) {
		t.Fatalf("SuperUser matched an unlisted id")
	}
	if !CheckGroup(30003)(ctxFor(t, groupMessage)) {
		t.Fatalf("CheckGroup did not match")
	}
	if CheckGroup(1, 2, 3)(ctxFor(t, groupMessage)) {
		t.Fatalf("CheckGroup matched wrong group")
	}
	if !CheckUser(20002)(ctxFor(t, privateMessage)) {
		t.Fatalf("CheckUser did not match")
	}
}

type fakeLookup struct {
	roles map[int64]string
	err   error
}

func (f fakeLookup) MemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.roles[userID], nil
}

func Test_F14_PermissionRulesAreFailClosed(t *testing.T) {
	t.Parallel()
	group := ctxFor(t, groupMessage)
	admin := fakeLookup{roles: map[int64]string{20002: "admin"}}
	owner := fakeLookup{roles: map[int64]string{20002: "owner"}}
	member := fakeLookup{roles: map[int64]string{20002: "member"}}

	if !GroupAdmin(admin)(group) {
		t.Fatalf("GroupAdmin should allow an admin")
	}
	if !GroupAdmin(owner)(group) {
		t.Fatalf("GroupAdmin should allow the owner")
	}
	if GroupAdmin(member)(group) {
		t.Fatalf("GroupAdmin must not allow a member")
	}
	if GroupAdmin(nil)(group) {
		t.Fatalf("GroupAdmin with nil lookup must fail closed")
	}
	if GroupAdmin(fakeLookup{err: errors.New("timeout")})(group) {
		t.Fatalf("GroupAdmin with lookup error must fail closed")
	}
	if !GroupOwner(owner)(group) {
		t.Fatalf("GroupOwner should allow the owner")
	}
	if GroupOwner(admin)(group) {
		t.Fatalf("GroupOwner must not allow an admin")
	}
	if GroupOwner(fakeLookup{err: errors.New("boom")})(group) {
		t.Fatalf("GroupOwner with lookup error must fail closed")
	}
}

func Test_F14_HigherThan(t *testing.T) {
	t.Parallel()
	group := ctxFor(t, groupMessage)
	lookup := fakeLookup{roles: map[int64]string{20002: "admin", 555: "member", 777: "owner"}}

	if !HigherThan(lookup, func(*Ctx) int64 { return 555 })(group) {
		t.Fatalf("admin should outrank member")
	}
	if HigherThan(lookup, func(*Ctx) int64 { return 777 })(group) {
		t.Fatalf("admin must not outrank owner")
	}
	if HigherThan(lookup, func(*Ctx) int64 { return 20002 })(group) {
		t.Fatalf("a user must not outrank themselves")
	}
	if HigherThan(fakeLookup{err: errors.New("boom")}, func(*Ctx) int64 { return 555 })(group) {
		t.Fatalf("lookup error must fail closed")
	}
}

func Test_F14_AllRulesSurviveNoticeEventWithoutSender(t *testing.T) {
	t.Parallel()
	c := ctxFor(t, noticeNoSender)
	lookup := fakeLookup{roles: map[int64]string{}}
	rules := []Rule{
		Kind("notice"), Prefix("x"), Suffix("x"), Keyword("x"), FullMatch("x"), Regex("x"),
		AtMe(), OnlyGroup(), OnlyPrivate(), OnlyToMe(),
		SuperUser(1), CheckUser(1), CheckGroup(1),
		GroupAdmin(lookup), GroupOwner(lookup), HigherThan(lookup, func(*Ctx) int64 { return 1 }),
		HasImage(), HasReply(), Never(), Always(),
	}
	for _, r := range rules {
		_ = r(c)
	}
}

func Test_F14_RegexpReturnsCompileError(t *testing.T) {
	t.Parallel()
	if _, err := Regexp("([a-z"); err == nil {
		t.Fatalf("Regexp with bad pattern: actual=nil expected=error")
	}
	if Regex("([a-z")(ctxFor(t, groupMessage)) {
		t.Fatalf("Regex with bad pattern must never match")
	}
	r, err := Regexp("^/ping ([a-z]+)$")
	if err != nil {
		t.Fatalf("Regexp: %v", err)
	}
	c := ctxFor(t, groupMessage)
	if !r(c) {
		t.Fatalf("precompiled regexp did not match")
	}
	m, ok := c.State[StateKeyRegexMatch]
	if !ok {
		t.Fatalf("regex submatches not written to state")
	}
	if sub, _ := m.([]string); len(sub) != 2 || sub[1] != "hello" {
		t.Fatalf("regex submatches: actual=%v", m)
	}
}
