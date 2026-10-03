package cost

import "context"

// Attribution 描述一次模型调用的归属：它属于哪个会话、哪个用户（F-66 的
// session/user 配额维度都靠它）。
//
// 归属经 ctx 传递，而不是加进方法签名：它是"这次调用属于谁"的横切信息，
// 与 internal/scope 里的记忆作用域同一性质；把 SessionKey 塞进 llm.ChatRequest
// 会污染协议形状，而协议形状是 llm 包唯一的对外契约。
type Attribution struct {
	SessionKey string
	UserID     string
}

// IsZero 报告归属是否为空（未注入）。
func (a Attribution) IsZero() bool { return a.SessionKey == "" && a.UserID == "" }

type attributionCtxKey struct{}

// WithAttribution 把归属放进 ctx。
//
// 会话与用户都为空时原样返回：省掉一次无意义的 ctx 包装。
func WithAttribution(ctx context.Context, sessionKey, userID string) context.Context {
	if sessionKey == "" && userID == "" {
		return ctx
	}
	return context.WithValue(ctx, attributionCtxKey{}, Attribution{SessionKey: sessionKey, UserID: userID})
}

// AttributionFrom 取出 ctx 里的归属；没有时返回零值。
//
// 返回零值而不是 error 是刻意的：未注入归属时计量照常走全局维度，
// 不该因为少了一个装饰器就把调用判成失败。
func AttributionFrom(ctx context.Context) Attribution {
	a, _ := ctx.Value(attributionCtxKey{}).(Attribution)
	return a
}
