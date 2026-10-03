package policy

import "testing"

// BenchmarkPolicyAllow 覆盖 F-77 的权限判定基准（含判定缓存）。
func BenchmarkPolicyAllow(b *testing.B) {
	p, err := LoadDefault()
	if err != nil {
		b.Fatalf("LoadDefault: %v", err)
	}
	roles := p.Roles()
	actions := p.Actions()
	if len(roles) == 0 || len(actions) == 0 {
		b.Skip("默认权限表为空")
	}
	role, action := roles[0], actions[0]
	_ = p.Allow(role, action) // 预热缓存
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Allow(role, action)
	}
}
