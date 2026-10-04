package policy

import "context"

type roleCtxKey struct{}

// WithRole 把已解析的角色放进 ctx（F-53 的执行侧硬拦截据此判定）。
//
// 角色经 ctx 传递而不是加进 Caller 的签名：传输层的契约是平台协议形状
// （action + params），不该为权限多一个参数；而"这次调用属于谁"本来就是上下文信息。
func WithRole(ctx context.Context, role string) context.Context {
	if role == "" {
		return ctx
	}
	return context.WithValue(ctx, roleCtxKey{}, role)
}

// RoleFrom 取出 ctx 里的角色；没有时返回空串（调用方按 everyone 处理，fail-closed）。
func RoleFrom(ctx context.Context) string {
	role, _ := ctx.Value(roleCtxKey{}).(string)
	return role
}
