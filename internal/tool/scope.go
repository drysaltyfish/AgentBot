package tool

import "context"

// turnScopeKey 是 ctx 里存放"当前这一轮属于谁"的私有键。
type turnScopeKey struct{}

// WithScope 把本轮的作用域放进 ctx。
//
// 作用域通常就是会话键。工具在执行时需要知道"这次调用属于哪个会话"——
// 例如按会话召回历史、按会话隔离记忆——而工具签名固定为 Execute(ctx, args)，
// 经 ctx 传递既不用改接口，也符合"上下文信息"的语义。
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, turnScopeKey{}, scope)
}

// ScopeFrom 取出 ctx 里的作用域；没有时返回空串。
func ScopeFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(turnScopeKey{}).(string); ok {
		return v
	}
	return ""
}
