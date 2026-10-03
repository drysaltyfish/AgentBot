package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenUpdateFlag 是触发重写的命令行标志名（FEATURES.md F-74）。
const goldenUpdateFlag = "update"

// goldenDir 是 golden 文件相对测试工作目录（包目录）的存放目录。
const goldenDir = "testdata"

func init() {
	// 只注册标志：不读文件、不起 goroutine（F-73 的 init 约束）。
	// 注册在包级 init 里，任何 import 本包的测试二进制都能接受 -update；
	// 若未注册，go test 会以"未知 flag"失败，CI 那条"必须因 CI 拒绝"的断言
	// 就永远无法命中（历史上正是这样形成假安全感的）。
	flag.Bool(goldenUpdateFlag, false, "重写 testdata 下的 golden 文件（CI 中禁止）")
}

// Update 报告当前是否应重写 golden 文件：仅当命令行传入 -update 且不在 CI 中。
//
// 正常情况下测试无需调用它：Assert 已内部处理 -update。它留给"只有重写时才想
// 额外做的事"（例如先构造昂贵的数据、再决定是否落盘）。
func Update() bool {
	return goldenUpdateRequested() && !inCI()
}

// Assert 把 got 与 testdata/<name>.golden 做归一化后的逐行比对。
//
// 行为（F-74）：
//   - 始终归一化行尾（CRLF/CR -> LF）并去掉每行尾部空白，因此 LF 与 CRLF
//     两种检出都能通过；
//   - 传入 -update 时重写 golden 文件并直接返回；
//   - 传入 -update 且处于 CI 环境时直接失败，避免在 CI 里悄悄改掉期望值。
//
// name 是相对 testdata/ 的路径（可含子目录），实际文件为 <name>.golden。
func Assert(t testing.TB, name string, got []byte) {
	t.Helper()
	if goldenUpdateRequested() {
		if inCI() {
			t.Fatalf("golden: -update 在 CI 中被拒绝（CI 环境禁止重写 golden 文件）：%s", name)
		}
		writeGolden(t, name, normalizeGolden(got))
		return
	}

	path := goldenPath(t, name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden 文件失败（%s）：%v；首次创建请运行 go test -update", path, err)
	}
	gotText := normalizeGolden(got)
	wantText := normalizeGolden(want)
	if gotText == wantText {
		return
	}
	reportGoldenDiff(t, name, wantText, gotText)
}

// AssertString 是 Assert 的字符串便利版本。
func AssertString(t testing.TB, name, got string) {
	t.Helper()
	Assert(t, name, []byte(got))
}

// goldenUpdateRequested 报告命令行是否传入了 -update。
//
// 通过 flag.Lookup 读取而不是持有一个包级 *bool，是为了不引入包级可变状态。
func goldenUpdateRequested() bool {
	f := flag.Lookup(goldenUpdateFlag)
	if f == nil {
		return false
	}
	getter, ok := f.Value.(flag.Getter)
	if !ok {
		return false
	}
	value, ok := getter.Get().(bool)
	return ok && value
}

// inCI 报告当前进程是否运行在 CI 中；GitHub Actions 会同时设置这两个变量。
func inCI() bool {
	for _, key := range []string{"CI", "GITHUB_ACTIONS"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v != "" && !strings.EqualFold(v, "false") {
			return true
		}
	}
	return false
}

// normalizeGolden 统一行尾并去掉每行尾部空白，再丢掉文件末尾的空行。
//
// 这是 F-74 的硬性要求：Windows 检出会把 LF 变成 CRLF，如果不归一化，
// golden 比对会"即红"，而开发者往往会因此去关掉测试。
func normalizeGolden(raw []byte) string {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// writeGolden 把归一化后的内容写回磁盘；必要时创建父目录。
func writeGolden(t testing.TB, name, content string) {
	t.Helper()
	path := goldenPath(t, name)
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("创建 golden 目录失败（%s）：%v", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatalf("重写 golden 文件失败（%s）：%v", path, err)
	}
}

// goldenPath 校验 name 并返回实际的 golden 文件路径。
func goldenPath(t testing.TB, name string) string {
	t.Helper()
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		t.Fatalf("非法的 golden 名称：%q", name)
	}
	return filepath.Join(goldenDir, clean+".golden")
}

// reportGoldenDiff 逐行指出差异，失败信息必须让人一眼看到"哪一行、期望什么、实际什么"。
func reportGoldenDiff(t testing.TB, name, want, got string) {
	t.Helper()
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	t.Errorf("golden 不匹配：%s.golden（期望 %d 行，实际 %d 行）",
		filepath.Join(goldenDir, name), len(wantLines), len(gotLines))
	const maxShown = 10
	shown := 0
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		t.Errorf("  第 %d 行：\n    期望：%q\n    实际：%q", i+1, w, g)
		shown++
		if shown >= maxShown {
			t.Errorf("  ...（差异超过 %d 行，其余省略）", maxShown)
			break
		}
	}
}
