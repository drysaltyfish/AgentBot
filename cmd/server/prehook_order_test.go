package main

import (
	"strings"
	"testing"
)

// Test_PreHookOrderContract 钉住"顺序即行为"这条契约的判定逻辑。
//
// 背景：引擎按注册顺序执行 pre 钩子，而"名单必须最先"这类顺序约束
// 曾经只写在 serve.go 的注释里——把它挪到别处照样编译、照样过测试，
// 表现却是被拉黑的人先被建了会话。现在契约是数据 + 启动期断言。
func Test_PreHookOrderContract(t *testing.T) {
	t.Parallel()

	ok := [][]string{
		{preHookAccess, preHookModeration, preHookAdmin}, // 全量
		{preHookAccess},                                // 只有名单
		{preHookAccess, preHookModeration},             // 没有超管
		{preHookAccess, preHookAdmin},                  // 审查关闭
		{"", preHookAccess, "", preHookModeration, ""}, // 未命名钩子不参与契约
		{"other", preHookAccess, preHookModeration},    // 契约外的钩子不算违规
	}
	for _, observed := range ok {
		if err := checkPreHookOrder(observed); err != nil {
			t.Fatalf("%v 应当合法: %v", observed, err)
		}
	}

	bad := []struct {
		observed []string
		want     string // 错误里应点出的钩子
	}{
		{[]string{"moderation", "access"}, "access"},    // 名单被排到审查之后
		{[]string{"admin", "moderation"}, "moderation"}, // 管理命令抢在审查之前
		{[]string{preHookModeration, preHookAccess, preHookAdmin}, preHookAccess},
	}
	for _, tc := range bad {
		err := checkPreHookOrder(tc.observed)
		if err == nil {
			t.Fatalf("%v 违反契约，应当报错", tc.observed)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("报错应点出 %q，实际: %v", tc.want, err)
		}
	}

	// 一个契约名都没匹配上也算失败：这说明装配改了名字而契约没跟上，
	// 此时遍历一个空集永远"顺序正确"——检查形同虚设。
	vacuum := [][]string{
		nil,
		{},
		{"", ""},
		{"renamed-access", "renamed-moderation"},
	}
	for _, observed := range vacuum {
		err := checkPreHookOrder(observed)
		if err == nil {
			t.Fatalf("%v 里没有任何契约钩子，应报错（否则改名会让检查静默失效）", observed)
		}
		if !strings.Contains(err.Error(), "一个都没注册") {
			t.Fatalf("报错应说明契约脱节，实际: %v", err)
		}
	}
}

// Test_PreHookOrderContractNamesTheRealHooks 防止契约与真实装配脱节。
//
// 契约与 serve.go 的注册引用同一批常量，所以改名会同时改到两边。
// 这条测试进一步钉住"契约恰好覆盖三个真实的引擎级 pre 钩子"——
// 少写一个会让那个钩子完全不被检查，多写一个则是永远不会匹配的幽灵。
func Test_PreHookOrderContractNamesTheRealHooks(t *testing.T) {
	t.Parallel()
	want := []string{preHookAccess, preHookModeration, preHookAdmin}
	if len(inboundPreHookOrder) != len(want) {
		t.Fatalf("契约应恰好覆盖三个引擎级 pre 钩子: %v", inboundPreHookOrder)
	}
	for i := range want {
		if inboundPreHookOrder[i] != want[i] {
			t.Fatalf("第 %d 项: actual=%q expected=%q", i, inboundPreHookOrder[i], want[i])
		}
	}
	// 名字必须互不相同，否则 rank 表会互相覆盖、契约形同虚设。
	seen := map[string]bool{}
	for _, name := range inboundPreHookOrder {
		if name == "" || seen[name] {
			t.Fatalf("钩子名必须非空且互不相同: %v", inboundPreHookOrder)
		}
		seen[name] = true
	}
}
