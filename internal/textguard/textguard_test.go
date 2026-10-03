package textguard

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func mustNew(t *testing.T, rules []Rule, opts Options) *Matcher {
	t.Helper()
	m, err := New(rules, opts)
	if err != nil {
		t.Fatalf("New(%v) 返回错误: %v", rules, err)
	}
	if m == nil {
		t.Fatal("New 不应返回 nil Matcher")
	}
	return m
}

// Test_F56_ChineseRuneOffsets 校验中文按字计下标、一次文本多词命中。
func Test_F56_ChineseRuneOffsets(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "暴力"}, {Word: "色情"}}, Options{})
	got := m.Find("不允许暴力与色情内容")
	want := []Match{
		{Word: "暴力", Start: 3, End: 5},
		{Word: "色情", Start: 6, End: 8},
	}
	if len(got) != len(want) {
		t.Fatalf("命中数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个命中 = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Test_F56_Overlapping 校验诊断用 Find 上报全部重叠，替换结果确定且互不重叠。
func Test_F56_Overlapping(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "abc"}, {Word: "bc"}, {Word: "bcd"}}, Options{})
	got := m.Find("abcd")
	want := []Match{
		{Word: "abc", Start: 0, End: 3},
		{Word: "bcd", Start: 1, End: 4},
		{Word: "bc", Start: 1, End: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("重叠命中数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个命中 = %+v, want %+v", i, got[i], want[i])
		}
	}

	// 重叠消解：ab... 同起点取最长，bcd/bc 与已选 [0,3) 重叠被跳过。
	rep := mustNew(t, []Rule{
		{Word: "abc", Replacement: "X"},
		{Word: "bc", Replacement: "Y"},
		{Word: "bcd", Replacement: "Z"},
	}, Options{})
	gotRep := rep.Replacements("abcd")
	if len(gotRep) != 1 || gotRep[0] != (Replacement{Start: 0, End: 3, Replacement: "X"}) {
		t.Fatalf("Replacements = %+v, want [{0 3 X}]", gotRep)
	}
	if s := rep.Replace("abcd"); s != "Xd" {
		t.Fatalf("Replace = %q, want %q", s, "Xd")
	}
}

// Test_F56_SameStartLongest 校验同起点取最长。
func Test_F56_SameStartLongest(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{
		{Word: "ab", Replacement: "short"},
		{Word: "abc", Replacement: "long"},
	}, Options{})
	got := m.Replacements("abc")
	want := Replacement{Start: 0, End: 3, Replacement: "long"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Replacements = %+v, want [%+v]", got, want)
	}
	if s := m.Replace("abc"); s != "long" {
		t.Fatalf("Replace = %q, want %q", s, "long")
	}
}

// Test_F56_Whitelist 校验被白名单区间覆盖的匹配不产生替换。
func Test_F56_Whitelist(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "敏感"}}, Options{Whitelist: []string{"敏感词"}})

	covered := "这是敏感词内容"
	if got := m.Replacements(covered); len(got) != 0 {
		t.Fatalf("白名单覆盖时不该产生替换，got %+v", got)
	}
	if got := m.Replace(covered); got != covered {
		t.Fatalf("白名单覆盖时 Replace 应原样返回，got %q", got)
	}
	if m.Has(covered) {
		t.Fatal("白名单覆盖时 Has 应为 false")
	}

	plain := "这是敏感内容"
	got := m.Replacements(plain)
	want := Replacement{Start: 2, End: 4, Replacement: "**"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("非白名单区 Replacements = %+v, want [%+v]", got, want)
	}
}

// Test_F56_Normalization 校验全角/半角、转义形态与大小写归一（仅查询侧）。
func Test_F56_Normalization(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "ABC"}}, Options{Normalize: true})
	escapeU := "\\u0041\\u0042\\u0043"
	escapeEntity := "&#x41;&#x42;&#x43;"
	for _, text := range []string{"ABC", "ＡＢＣ", escapeU, escapeEntity} {
		if !m.Has(text) {
			t.Fatalf("归一后应命中 %q", text)
		}
	}
	if m.Has("abc") {
		t.Fatal("未开启大小写不敏感时 abc 不该命中 ABC")
	}

	// 全角词表 + 半角文本。
	fw := mustNew(t, []Rule{{Word: "ＡＢＣ"}}, Options{Normalize: true})
	if !fw.Has("ABC") {
		t.Fatal("全角词表应命中半角文本")
	}

	// 转义匹配映射回原文 rune 下标：x + 3 个转义 + y。
	esc := "x" + escapeU + "y"
	got := m.Replacements(esc)
	wantMask := strings.Repeat("*", 18)
	want := Replacement{Start: 1, End: 19, Replacement: wantMask}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("转义映射 = %+v, want [%+v]", got, want)
	}
	wantText := "x" + wantMask + "y"
	if s := m.Replace(esc); s != wantText {
		t.Fatalf("转义 Replace = %q, want %q", s, wantText)
	}
}

// Test_F56_CaseSensitivity 校验默认大小写敏感、可选不敏感。
func Test_F56_CaseSensitivity(t *testing.T) {
	t.Parallel()
	def := mustNew(t, []Rule{{Word: "Bad"}}, Options{})
	if def.Has("bad") {
		t.Fatalf("默认应大小写敏感：bad 不该命中 Bad")
	}
	if !def.Has("Bad") {
		t.Fatalf("默认应命中原样大小写")
	}

	ci := mustNew(t, []Rule{{Word: "Bad"}}, Options{CaseInsensitive: true})
	for _, text := range []string{"BAD", "bad", "bAd"} {
		got := ci.Find(text)
		if len(got) != 1 || got[0].Word != "Bad" {
			t.Fatalf("大小写不敏感应命中 %q，got %+v", text, got)
		}
	}
	zh := mustNew(t, []Rule{{Word: "敏感"}}, Options{CaseInsensitive: true})
	if !zh.Has("敏感") {
		t.Fatalf("中文应照常命中")
	}
}

// Test_F56_RejectBadPatterns 校验空模式与超长模式在构建期返回 error。
func Test_F56_RejectBadPatterns(t *testing.T) {
	t.Parallel()
	if _, err := New([]Rule{{Word: ""}}, Options{}); err == nil {
		t.Fatal("空模式应返回 error")
	}
	if _, err := New([]Rule{{Word: "   "}}, Options{}); err == nil {
		t.Fatal("纯空白模式应返回 error")
	}
	if _, err := New([]Rule{{Word: strings.Repeat("a", 65)}}, Options{}); err == nil {
		t.Fatal("默认上限 64，65 应返回 error")
	}
	if _, err := New([]Rule{{Word: strings.Repeat("a", 64)}}, Options{}); err != nil {
		t.Fatalf("恰好 64 应被接受，got %v", err)
	}
	if _, err := New([]Rule{{Word: strings.Repeat("a", 9)}}, Options{MaxPatternRunes: 8}); err == nil {
		t.Fatal("可配上限 8，9 应返回 error")
	}
	if _, err := New([]Rule{{Word: strings.Repeat("a", 8)}}, Options{MaxPatternRunes: 8}); err != nil {
		t.Fatalf("恰好 8 应被接受，got %v", err)
	}
	if _, err := New(nil, Options{Whitelist: []string{""}}); err == nil {
		t.Fatal("白名单空模式应返回 error")
	}
}

// Test_F56_EmptyWordList 校验空词表返回可用对象且永不命中。
func Test_F56_EmptyWordList(t *testing.T) {
	t.Parallel()
	m := mustNew(t, nil, Options{})
	if got := m.Find("任意文本"); len(got) != 0 {
		t.Fatalf("空词表不该命中，got %+v", got)
	}
	if m.Has("任意文本") {
		t.Fatal("空词表 Has 必须为 false")
	}
	if got := m.Replace("任意文本"); got != "任意文本" {
		t.Fatalf("空词表 Replace 应原样返回，got %q", got)
	}
	if got := m.Replacements("任意文本"); len(got) != 0 {
		t.Fatalf("空词表 Replacements 应为空，got %+v", got)
	}
	if ws := m.Words(); len(ws) != 0 {
		t.Fatalf("空词表 Words 应为空，got %v", ws)
	}
}

// Test_F56_NoMatch 校验无命中返回空。
func Test_F56_NoMatch(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "敏感"}}, Options{})
	if got := m.Find("这是一段普通文本"); len(got) != 0 {
		t.Fatalf("无命中应返回空，got %+v", got)
	}
	if m.Has("这是一段普通文本") {
		t.Fatal("无命中 Has 应为 false")
	}
}

// Test_F56_Replace 校验默认掩码与逐词替换串。
func Test_F56_Replace(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "暴力"}, {Word: "色情"}}, Options{})
	if got := m.Replace("不允许暴力与色情内容"); got != "不允许**与**内容" {
		t.Fatalf("Replace = %q, want %q", got, "不允许**与**内容")
	}
	if got := m.Replace("干净文本"); got != "干净文本" {
		t.Fatalf("无命中 Replace 应原样返回，got %q", got)
	}

	per := mustNew(t, []Rule{
		{Word: "暴力", Replacement: "[A]"},
		{Word: "色情", Replacement: "[B]"},
	}, Options{})
	if got := per.Replace("暴力与色情"); got != "[A]与[B]" {
		t.Fatalf("逐词替换 = %q, want %q", got, "[A]与[B]")
	}

	def := mustNew(t, []Rule{{Word: "暴力"}, {Word: "色情", Replacement: "[B]"}},
		Options{DefaultReplacement: "#"})
	if got := def.Replace("暴力色情"); got != "#[B]" {
		t.Fatalf("全局默认替换 = %q, want %q", got, "#[B]")
	}

	// 从后往前重建：前面的替换改变长度也不能破坏后面的下标。
	multi := mustNew(t, []Rule{
		{Word: "一", Replacement: "11"},
		{Word: "二", Replacement: "22"},
	}, Options{})
	if got := multi.Replace("一三二"); got != "11三22" {
		t.Fatalf("从后往前重建 = %q, want %q", got, "11三22")
	}
}

// Test_F56_WordsDedupSort 校验词表去重且稳定排序，返回拷贝。
func Test_F56_WordsDedupSort(t *testing.T) {
	t.Parallel()
	m := mustNew(t, []Rule{{Word: "b"}, {Word: "a"}, {Word: "b"}, {Word: "a"}}, Options{})
	want := []string{"a", "b"}
	got := m.Words()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Words = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if again := m.Words(); again[0] != "a" {
		t.Fatalf("Words 必须返回拷贝，内部被污染: %v", again)
	}
	ci := mustNew(t, []Rule{{Word: "Ab"}, {Word: "ab"}, {Word: "AB"}}, Options{CaseInsensitive: true})
	if ws := ci.Words(); len(ws) != 1 || ws[0] != "Ab" {
		t.Fatalf("大小写不敏感去重 = %v, want [Ab]", ws)
	}
}

// Test_F56_ConcurrentFind 校验并发只读查询无数据竞争。
func Test_F56_ConcurrentFind(t *testing.T) {
	t.Parallel()
	rules := make([]Rule, 0, 100)
	for i := 0; i < 100; i++ {
		rules = append(rules, Rule{Word: fmt.Sprintf("敏感词%03d", i)})
	}
	m := mustNew(t, rules, Options{CaseInsensitive: true})
	text := "前言 abc 敏感词007 结尾 敏感词099"
	want := len(m.Find(text))
	if want != 2 {
		t.Fatalf("基准命中数 = %d, want 2", want)
	}

	const goroutines = 32
	errCh := make(chan string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if got := len(m.Find(text)); got != want {
					errCh <- fmt.Sprintf("Find 命中数 = %d, want %d", got, want)
					return
				}
				if !m.Has(text) {
					errCh <- "Has 应为 true"
					return
				}
				if reps := m.Replacements(text); len(reps) != 2 {
					errCh <- fmt.Sprintf("Replacements 数 = %d, want 2", len(reps))
					return
				}
				if r := m.Replace(text); !strings.Contains(r, "**") {
					errCh <- fmt.Sprintf("Replace 未掩码: %q", r)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}

// Test_F56_EngineSwap 校验热加载原子替换后的行为。
func Test_F56_EngineSwap(t *testing.T) {
	t.Parallel()
	oldM := mustNew(t, []Rule{{Word: "暴力", Replacement: "[old]"}}, Options{})
	newM := mustNew(t, []Rule{{Word: "色情", Replacement: "[new]"}}, Options{})
	e := NewEngine(oldM)
	if e.Load() != oldM {
		t.Fatal("Load 应返回初始 Matcher")
	}
	if !e.Has("暴力") || e.Has("色情") {
		t.Fatal("替换前应只命中旧词表")
	}
	e.Swap(newM)
	if e.Load() != newM {
		t.Fatal("Swap 后 Load 应返回新 Matcher")
	}
	if e.Has("暴力") {
		t.Fatal("替换后旧词表不应命中")
	}
	if got := e.Replace("色情内容"); got != "[new]内容" {
		t.Fatalf("替换后 Replace = %q, want %q", got, "[new]内容")
	}

	// 并发 Swap + 查询不应出现半成品。
	const workers = 16
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if k%2 == 0 {
					e.Swap(oldM)
				} else {
					e.Swap(newM)
				}
				_ = e.Replacements("暴力与色情内容")
			}
		}(g)
	}
	wg.Wait()
}

// Test_F56_LargeDict 校验约 1 万词表下查询正确。
func Test_F56_LargeDict(t *testing.T) {
	t.Parallel()
	m := mustNew(t, largeRules(10000), Options{})
	text := largeText(1024)
	got := m.Find(text)
	if len(got) == 0 {
		t.Fatalf("大词表应在构造文本中命中")
	}
	if got[0].Word != "敏感词00042" || got[0].Start != 100 {
		t.Fatalf("首个命中 = %+v, want 敏感词00042@100", got[0])
	}
	if !m.Has(text) {
		t.Fatal("大词表 Has 应为 true")
	}
}

// BenchmarkMatchLargeDict 度量 1 万词表对约 1KB 文本的查询吞吐。
func BenchmarkMatchLargeDict(b *testing.B) {
	m, err := New(largeRules(10000), Options{})
	if err != nil {
		b.Fatal(err)
	}
	text := largeText(1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(m.Replacements(text)) == 0 {
			b.Fatal("应命中")
		}
	}
}

func largeRules(n int) []Rule {
	rules := make([]Rule, 0, n)
	for i := 0; i < n; i++ {
		rules = append(rules, Rule{Word: fmt.Sprintf("敏感词%05d", i)})
	}
	return rules
}

// largeText 构造约 n 个 rune 的文本，并在下标 100 处埋入一个词表命中。
func largeText(n int) string {
	base := []rune("这是用于压测敏感词引擎的中文文本内容")
	out := make([]rune, 0, n)
	for len(out) < n {
		out = append(out, base...)
	}
	out = out[:n]
	copy(out[100:], []rune("敏感词00042"))
	return string(out)
}

// BenchmarkACReplace 是 F-77 点名的基准：1 万词表下的替换（AC 自动机 + 重叠消解）。
func BenchmarkACReplace(b *testing.B) {
	words := make([]string, 0, 10000)
	for i := 0; i < 10000; i++ {
		words = append(words, "敏感词"+strconv.Itoa(i))
	}
	rules := make([]Rule, 0, len(words))
	for _, w := range words {
		rules = append(rules, Rule{Word: w, Replacement: "***"})
	}
	m, err := New(rules, Options{DefaultReplacement: "***"})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	text := strings.Repeat("这是一段普通聊天内容，夹杂敏感词42和敏感词9999。", 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Replace(text)
	}
}
