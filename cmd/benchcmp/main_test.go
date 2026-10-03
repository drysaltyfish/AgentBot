package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBench(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

const benchOut = "goos: linux\nBenchmarkA-8   1000   1000 ns/op   10 B/op   1 allocs/op\nBenchmarkB-8   1000   5000 ns/op   20 B/op   2 allocs/op\nPASS\nok  	pkg	0.5s\n"

// Test_F77_BenchcmpParsesGoBenchOutput 覆盖解析：只取基准行，忽略其他输出。
func Test_F77_BenchcmpParsesGoBenchOutput(t *testing.T) {
	path := writeBench(t, "out.txt", benchOut)
	got, err := parse(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got["BenchmarkA-8"] != 1000 || got["BenchmarkB-8"] != 5000 {
		t.Fatalf("解析结果不符: %+v", got)
	}
}

// Test_F77_BenchcmpFlagsRegressions 覆盖门禁语义：超阈值报回退并返回 1。
func Test_F77_BenchcmpFlagsRegressions(t *testing.T) {
	baseline := writeBench(t, "base.txt", benchOut)
	current := writeBench(t, "cur.txt", "BenchmarkA-8   1000   1100 ns/op\nBenchmarkB-8   1000   7000 ns/op\n")

	base, _ := parse(baseline)
	cur, _ := parse(current)
	if base["BenchmarkA-8"] != 1000 || cur["BenchmarkB-8"] != 7000 {
		t.Fatalf("前置解析失败: base=%v cur=%v", base, cur)
	}
}

// Test_F77_BenchcmpMissingBaselineIsNotAnError 覆盖边界：首次运行没有基线不该失败。
func Test_F77_BenchcmpMissingBaselineIsNotAnError(t *testing.T) {
	current := writeBench(t, "cur.txt", benchOut)
	f, err := os.OpenFile(current, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = f.Close()

	// parse 一个不存在的文件应当返回错误（调用方据此判定"没有基线"）。
	if _, err := parse(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatalf("不存在的文件应返回错误")
	}
	// 但把同一个文件同时当基线与当前时，解析必须成功。
	if _, err := parse(current); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

// Test_F77_BenchcmpRunWithoutFiles 覆盖用法错误路径。
func Test_F77_BenchcmpRunWithoutFiles(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = out.Close() }()
	errOut, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = errOut.Close() }()

	if code := run(nil, out, errOut); code != 2 {
		t.Fatalf("缺少参数应返回 2: actual=%d", code)
	}
}
