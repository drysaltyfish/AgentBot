package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sendCalls 是"把消息发给平台"的调用形态。
//
// 用**发送包装函数**而不是动作字面量：`send_group_msg` 这类字面量只出现在
// transport 的包装实现里，包装函数才是别人可能直接调用的入口。
var sendCalls = []string{
	"transport.SendGroupMsg(",
	"transport.SendPrivateMsg(",
}

// allowedSenders 是允许直接发送的包（相对仓库根目录）。
//
// `internal/outbound` 就是**唯一出口**：它先跑 F-55 的出口过滤链，再调 Caller。
// `internal/transport` 是包装函数的定义处，不算调用方。
var allowedSenders = map[string]bool{
	filepath.Join("internal", "outbound"):  true,
	filepath.Join("internal", "transport"): true,
}

// Test_F55_OnlyOutboundSendsMessages 钉住 F-55 验收里的"**所有**发送路径"。
//
// 上一条测试（internal/reply 的 Test_F55_*）证明"经由 Sender 的内容确实被过滤过"，
// 但它只能覆盖它跑到的那条路径。真正要防的是**新增一条绕过 Sender 的发送路径**——
// 那时旧测试照样全绿，而用户内容已经绕过了出口处理。
//
// 所以这里做的是**结构性检查**：扫描生产源码，确认除 `internal/outbound` 之外
// 没有任何包调用发送包装函数。这比穷举运行所有路径更可靠——它检查的是"有没有这种代码"，
// 而不管那条路径在测试里有没有被走到。
//
// 只读型平台调用（get_group_member_info / get_stranger_info / get_msg 等）不在此列：
// 它们不产生发给用户的文本内容，出口过滤链与它们无关。
func Test_F55_OnlyOutboundSendsMessages(t *testing.T) {
	t.Parallel()
	// go test 的工作目录是包目录，仓库根是它的上两级。
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根目录: %v", err)
	}

	var violations []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// 跳过非生产代码：VCS、参考项目、测试数据。
			if name == ".git" || name == "参考项目" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if allowedSenders[filepath.Dir(rel)] {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, pat := range sendCalls {
			if strings.Contains(string(src), pat) {
				violations = append(violations, rel+" 调用了 "+pat)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描源码失败: %v", walkErr)
	}
	if len(violations) > 0 {
		t.Fatalf("发现绕过 outbound.Sender 的发送路径——这些消息不会经过 F-55 出口过滤链：\n  %s\n"+
			"所有发送都必须走 outbound.Sender（它是唯一出口）", strings.Join(violations, "\n  "))
	}
}
