package scope

import "context"

// subjectKey 是"这条记忆关于谁"的 ctx 键。
//
// 与作用域（scopeKey）是**两个维度**：作用域决定"谁看得到"（群 A 的记忆不进群 B），
// 归属人决定"这是谁的事"（张三的事不答给李四）。记忆按群共享是对的——同一群的人
// 需要共同上下文；但一条事实总有归属人，把它做成结构化字段比让模型在自然语言里
// 自己写主语可靠得多。
type subjectKey struct{}

// WithSubject 把"记忆归属人"（群聊里是发言人 QQ 号）放进 ctx。
//
// id <= 0 时视为未指明，不写键——避免用 0 冒充一个真实用户。
func WithSubject(ctx context.Context, id int64) context.Context {
	if id <= 0 {
		return ctx
	}
	return context.WithValue(ctx, subjectKey{}, id)
}

// WithoutSubject 显式把归属清空：用于**公共记忆**（活动通知、群规、共同决定）。
//
// 与"没设置过"的区别是有意的：没设置 = 跟随调用上下文（通常是发言人），
// 显式清空 = 这条事实不属于任何个人。
func WithoutSubject(ctx context.Context) context.Context {
	return context.WithValue(ctx, subjectKey{}, int64(0))
}

// SubjectFrom 取出 ctx 里的归属人；没有时返回 0（未指明）。
func SubjectFrom(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(subjectKey{}).(int64); ok && v > 0 {
		return v
	}
	return 0
}
