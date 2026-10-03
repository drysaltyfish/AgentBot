// Package scope 拥有"当前这一轮属于谁"这个上下文键。
//
// 作用域通常就是会话键：按会话召回历史、按会话隔离记忆，都需要在执行时知道
// "这次调用属于哪个会话"。工具的执行签名固定为 Execute(ctx, args)，把它加进
// 签名会波及所有工具，而作用域本来就是上下文信息——因此经 ctx 传递。
//
// 这个包存在的意义是**只有一份键的定义**：记忆模块、工具实现与 agent 都通过
// 它读写同一个键。若各自定义私有键，ctx 里的值就取不出来，隔离会静默失效。
package scope

import "context"

// scopeKey 是 ctx 里存放作用域的私有键。
//
// 键必须是同一个值才能互相取值，因此它只在本包定义一次，别处一律经
// WithScope / ScopeFrom 访问。
type scopeKey struct{}

// WithScope 把本轮的作用域放进 ctx。
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFrom 取出 ctx 里的作用域；没有时返回空串。
//
// ctx 为 nil 时也返回空串，调用方无需先判空。
func ScopeFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(scopeKey{}).(string); ok {
		return v
	}
	return ""
}
