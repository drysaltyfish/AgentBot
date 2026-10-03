// Command benchcmp 比较两次 go test -bench 输出，找出回退超过阈值的基准（F-77）。
//
// 决策 16 明确"不做 Makefile"，因此 F-77 里的 make bench-compare 用这个命令等价实现：
//
//	go test -run '^$' -bench . -benchmem ./... > baseline.txt
//	go test -run '^$' -bench . -benchmem ./... > current.txt
//	go run ./cmd/benchcmp -baseline baseline.txt -current current.txt
//
// 只比较 ns/op；分配数据不参与门禁（抖动更大）。没有同名基准可比对时退出码为 0。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
)

// benchLine 匹配 go test -bench 的输出行，例如：
//
//	BenchmarkRouteMatch-16   1000   27400 ns/op   8232 B/op   3 allocs/op
var benchLine = regexp.MustCompile("^(Benchmark\\S+)\\s+\\d+\\s+([0-9.]+)\\s+ns/op")

// parse 读取 go test -bench 输出，返回 基准名 -> ns/op。
func parse(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := map[string]float64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := benchLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v, perr := strconv.ParseFloat(m[2], 64)
		if perr != nil {
			continue
		}
		out[m[1]] = v
	}
	return out, sc.Err()
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("benchcmp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baselinePath := flags.String("baseline", "", "基线 go test -bench 输出文件")
	currentPath := flags.String("current", "", "当前 go test -bench 输出文件")
	threshold := flags.Float64("threshold", 0.20, "回退告警阈值（0.20 = 20%）")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *baselinePath == "" || *currentPath == "" {
		_, _ = fmt.Fprintln(stderr, "用法: benchcmp -baseline <file> -current <file> [-threshold 0.20]")
		return 2
	}

	baseline, err := parse(*baselinePath)
	if err != nil {
		// 没有基线不是错误：第一次跑就是没有可比对象。
		_, _ = fmt.Fprintf(stdout, "没有可用的基线（%v）；跳过对比。\n", err)
		return 0
	}
	current, err := parse(*currentPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "读取当前结果失败: %v\n", err)
		return 1
	}

	names := make([]string, 0, len(current))
	for n := range current {
		names = append(names, n)
	}
	sort.Strings(names)

	regressions, compared := 0, 0
	for _, n := range names {
		base, ok := baseline[n]
		if !ok || base <= 0 {
			continue
		}
		compared++
		cur := current[n]
		delta := (cur - base) / base
		verdict := "ok"
		if delta > *threshold {
			verdict = "REGRESSION"
			regressions++
		}
		_, _ = fmt.Fprintf(stdout, "%-40s %10.0f -> %10.0f ns/op  %+6.1f%%  %s\n", n, base, cur, delta*100, verdict)
	}

	if compared == 0 {
		_, _ = fmt.Fprintln(stdout, "两组结果没有同名基准，跳过对比。")
		return 0
	}
	if regressions > 0 {
		_, _ = fmt.Fprintf(stdout, "\n%d/%d 个基准回退超过 %.0f%%\n", regressions, compared, *threshold*100)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "\n%d 个基准均在阈值内（<= %.0f%%）\n", compared, *threshold*100)
	return 0
}
