package testutil

import "testing"

// Test_F74_GoldenNormalizeIsCRLFAndWhitespaceStable 钉住 F-74 的硬性归一化规则：
// LF 与 CRLF 检出必须得到同一份文本，且每行尾部空白与文件末尾空行不参与比对。
func Test_F74_GoldenNormalizeIsCRLFAndWhitespaceStable(t *testing.T) {
	t.Parallel()
	crlf := []byte("a  \r\nb\t\r\nc\r\n\r\n")
	lf := []byte("a\nb\nc\n")
	if got, want := normalizeGolden(crlf), normalizeGolden(lf); got != want {
		t.Fatalf("CRLF 与 LF 归一化结果不一致：actual=%q expected=%q", got, want)
	}
	if got, want := normalizeGolden([]byte("a  \nb\t\n\n")), "a\nb"; got != want {
		t.Fatalf("行尾空白/末尾空行未去除：actual=%q expected=%q", got, want)
	}
}

// Test_F74_GoldenCIDetection 钉住 CI 判定，供 -update 拒绝逻辑使用。
func Test_F74_GoldenCIDetection(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "")
	if !inCI() {
		t.Fatalf("CI=true 时必须判定为 CI 环境")
	}
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "true")
	if !inCI() {
		t.Fatalf("GITHUB_ACTIONS=true 时必须判定为 CI 环境")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	if inCI() {
		t.Fatalf("未设置 CI 变量时不应判定为 CI 环境")
	}
}
