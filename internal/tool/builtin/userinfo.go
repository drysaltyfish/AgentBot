package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/tool"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// CallerProvider 提供平台 API 通道。
//
// 用**提供者**而不是直接持有 Caller：传输客户端在工具装配之后才创建，
// 直接注入会迫使初始化顺序倒置（而顺序一乱，很容易再踩一次"在错误的地方调 API"）。
type CallerProvider interface {
	Caller() transport.Caller
}

type getUserInfo struct{ deps Deps }

func (getUserInfo) Name() string { return "get_user_info" }
func (getUserInfo) Description() string {
	return "按 QQ 号查询这个人的群名片、昵称、头像链接与群内身份；用于确认消息里的 QQ 号是谁"
}
func (getUserInfo) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{
		"qq": {Type: "integer", Description: "要查询的 QQ 号"},
	}, Required: []string{"qq"}}
}
func (getUserInfo) ReadOnly() bool        { return true }
func (getUserInfo) ConcurrencySafe() bool { return true }

type getUserInfoArgs struct {
	QQ int64 `arg:"qq"`
}

// avatarURL 返回 QQ 头像链接。
//
// 头像不需要调 API：QQ 的头像地址是约定好的，拼出来即可。
func avatarURL(qq int64) string {
	return fmt.Sprintf("https://q1.qlogo.cn/g?b=qq&nk=%d&s=640", qq)
}

func (t getUserInfo) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[getUserInfoArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if in.QQ <= 0 {
		return tool.Failure("qq 必须是正整数"), nil
	}
	if t.deps.Caller == nil || t.deps.Caller.Caller() == nil {
		return tool.Failure("平台 API 通道不可用，暂时查不到用户信息"), nil
	}

	groupID := groupIDFromScope(tool.ScopeFrom(ctx))
	if groupID != 0 {
		return t.groupMember(ctx, groupID, in.QQ)
	}
	return t.stranger(ctx, in.QQ)
}

func (t getUserInfo) groupMember(ctx context.Context, groupID, qq int64) (tool.Result, error) {
	resp, err := t.deps.Caller.Caller().Call(ctx, transport.Request{
		Action: "get_group_member_info",
		Params: map[string]any{"group_id": groupID, "user_id": qq, "no_cache": false},
	})
	if err != nil {
		return tool.Failure(fmt.Sprintf("查询群成员失败: %v", err)), nil
	}
	if resp.RetCode != 0 {
		return tool.Failure(fmt.Sprintf("查询群成员失败: retcode=%d %s", resp.RetCode, resp.Message)), nil
	}
	var info struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
		Role     string `json:"role"`
		Level    string `json:"level"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return tool.Failure(fmt.Sprintf("解析群成员信息失败: %v", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "QQ: %d", qq)
	if info.Card != "" {
		fmt.Fprintf(&b, "`n群名片: %s", info.Card)
	}
	if info.Nickname != "" {
		fmt.Fprintf(&b, "`n昵称: %s", info.Nickname)
	}
	if role := roleLabel(info.Role); role != "" {
		fmt.Fprintf(&b, "`n群内身份: %s", role)
	}
	if info.Level != "" {
		fmt.Fprintf(&b, "`n等级: %s", info.Level)
	}
	if info.Title != "" {
		fmt.Fprintf(&b, "`n专属头衔: %s", info.Title)
	}
	fmt.Fprintf(&b, "`n头像: %s", avatarURL(qq))
	return tool.Success(b.String()), nil
}

func (t getUserInfo) stranger(ctx context.Context, qq int64) (tool.Result, error) {
	resp, err := t.deps.Caller.Caller().Call(ctx, transport.Request{
		Action: "get_stranger_info",
		Params: map[string]any{"user_id": qq, "no_cache": false},
	})
	if err != nil {
		return tool.Failure(fmt.Sprintf("查询用户失败: %v", err)), nil
	}
	if resp.RetCode != 0 {
		return tool.Failure(fmt.Sprintf("查询用户失败: retcode=%d %s", resp.RetCode, resp.Message)), nil
	}
	var info struct {
		Nickname string `json:"nickname"`
		Sex      string `json:"sex"`
		Age      int    `json:"age"`
	}
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return tool.Failure(fmt.Sprintf("解析用户信息失败: %v", err)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "QQ: %d", qq)
	if info.Nickname != "" {
		fmt.Fprintf(&b, "\n昵称: %s", info.Nickname)
	}
	fmt.Fprintf(&b, "\n头像: %s", avatarURL(qq))
	return tool.Success(b.String()), nil
}

func roleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return "群主"
	case "admin":
		return "管理员"
	case "member":
		return "普通成员"
	default:
		return ""
	}
}

// groupIDFromScope 从会话作用域里取出群号（私聊为 0）。
//
// 作用域格式是 "selfID:groupID:userID"（见 session.Key.String）。
func groupIDFromScope(scope string) int64 {
	parts := strings.Split(scope, ":")
	if len(parts) < 2 {
		return 0
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
