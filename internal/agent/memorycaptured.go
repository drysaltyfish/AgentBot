package agent

import "context"

// memoryCapturedKey 标记"本轮的记忆已由规则触发写入"。
type memoryCapturedKey struct{}

// WithMemoryCaptured 标记本轮已捕获记忆，避免模型再保存一次同样的内容。
//
// 真实事故：用户说"记住：我喜欢喝橙汁"时，规则触发先写入，
// 模型随后又调用 save_memory 把同一件事改写成第三人称再写一遍。
// 只靠存储层去重不够——措辞不同就会漏网，所以要在源头掐断。
func WithMemoryCaptured(ctx context.Context) context.Context {
	return context.WithValue(ctx, memoryCapturedKey{}, true)
}

// MemoryCaptured 报告本轮是否已由规则触发捕获记忆。
func MemoryCaptured(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(memoryCapturedKey{}).(bool)
	return v
}
