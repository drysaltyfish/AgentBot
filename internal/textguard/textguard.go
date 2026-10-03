// Package textguard 提供基于 Aho–Corasick 自动机的敏感词引擎。
//
// 对应 FEATURES.md F-56：
//   - 构建期插入全部模式并 BFS 构建 fail 指针；构建完成后 Matcher 不可变，可多 goroutine 无锁并发查询；
//   - 查询产物是 []Replacement（rune 下标、左闭右开），支持每个词各自的替换串；
//   - 重叠消解：按 Start 升序、同起点取最长，跳过与已选区间重叠的匹配；
//   - 白名单自动机剔除命中白名单区间的匹配；
//   - 归一仅在查询侧进行（ASCII 大小写、全角/半角、\uXXXX 与 &#xXX; 转义），不改变原文；
//   - 空模式与超长模式（默认 > 64 rune）在构建期返回 error；
//   - 词表热加载通过 Engine（atomic.Pointer[Matcher]）原子替换指针，不阻塞查询。
package textguard

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// defaultMaxPatternRunes 是单条模式允许的最大 rune 数。
const defaultMaxPatternRunes = 64

// Replacement 描述一次替换：原文 rune 区间 [Start, End) 替换为 Replacement。
type Replacement struct {
	Start, End  int
	Replacement string
}

// Match 是一次原始命中（含重叠），Start/End 为 rune 下标、左闭右开。
type Match struct {
	Word       string
	Start, End int
}

// Rule 是一条敏感词规则；Replacement 为空时回退到 Options.DefaultReplacement。
type Rule struct {
	Word        string
	Replacement string
}

// Options 控制 Matcher 的构建与查询行为。
type Options struct {
	CaseInsensitive    bool     // ASCII 大小写不敏感
	Normalize          bool     // 全角/半角与转义形态归一（仅查询侧）
	Whitelist          []string // 命中这些区间的匹配不产生替换
	MaxPatternRunes    int      // 单模式最大 rune 数，<=0 时取默认 64
	DefaultReplacement string   // 全局默认替换串，为空时按原文长度以 '*' 掩码
}

// acOutput 是挂在自动机节点上的一个完整模式。
type acOutput struct {
	word        string // 对外展示的原始词形
	replacement string // 该词专属替换串，可为空
	runes       int    // 归一化后的模式长度（rune 数）
}

// acNode 是自动机的一个状态。
type acNode struct {
	children map[rune]*acNode
	fail     *acNode
	outputs  []acOutput
}

// automaton 是一棵构建完成的只读 AC 自动机。
type automaton struct {
	root *acNode
}

func newAutomaton() *automaton {
	return &automaton{root: &acNode{children: make(map[rune]*acNode)}}
}

func (a *automaton) insert(key []rune, out acOutput) {
	node := a.root
	for _, r := range key {
		next, ok := node.children[r]
		if !ok {
			next = &acNode{children: make(map[rune]*acNode)}
			node.children[r] = next
		}
		node = next
	}
	node.outputs = append(node.outputs, out)
}

// build 用 BFS 填 fail 指针，并把 fail 链上的输出并入本节点，
// 使查询期无需再沿 fail 回溯输出。
func (a *automaton) build() {
	queue := make([]*acNode, 0, 64)
	for _, child := range a.root.children {
		child.fail = a.root
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		if node.fail != nil && len(node.fail.outputs) > 0 {
			node.outputs = append(node.outputs, node.fail.outputs...)
		}
		for r, child := range node.children {
			fail := node.fail
			for fail != nil {
				if next, ok := fail.children[r]; ok {
					child.fail = next
					break
				}
				fail = fail.fail
			}
			if child.fail == nil {
				child.fail = a.root
			}
			queue = append(queue, child)
		}
	}
}

// hit 是自动机在归一化 rune 序列上的一次命中。
type hit struct {
	start, end int // 归一化 rune 区间，左闭右开
	out        acOutput
}

func (a *automaton) scan(runes []rune) []hit {
	if a == nil || len(runes) == 0 {
		return nil
	}
	var hits []hit
	node := a.root
	for i, r := range runes {
		node = a.step(node, r)
		for _, out := range node.outputs {
			hits = append(hits, hit{start: i + 1 - out.runes, end: i + 1, out: out})
		}
	}
	return hits
}

func (a *automaton) step(node *acNode, r rune) *acNode {
	for node != a.root {
		if _, ok := node.children[r]; ok {
			break
		}
		node = node.fail
	}
	if next, ok := node.children[r]; ok {
		return next
	}
	return a.root
}

// Matcher 是构建完成的敏感词自动机；零值不可用，请用 New 构造。
// 构建返回后所有字段只读，可被多 goroutine 无锁并发查询。
type Matcher struct {
	main               *automaton
	white              *automaton
	caseInsensitive    bool
	normalize          bool
	defaultReplacement string
	words              []string
}

// New 校验并使用 rules 构建自动机。
//
// 空模式（含纯空白）与超过 MaxPatternRunes（默认 64）的模式返回 error，不 panic。
// 归一化后键相同的规则只保留第一条。opts.Whitelist 用同一套归一化构建白名单自动机。
func New(rules []Rule, opts Options) (*Matcher, error) {
	maxRunes := opts.MaxPatternRunes
	if maxRunes <= 0 {
		maxRunes = defaultMaxPatternRunes
	}
	m := &Matcher{
		main:               newAutomaton(),
		white:              newAutomaton(),
		caseInsensitive:    opts.CaseInsensitive,
		normalize:          opts.Normalize,
		defaultReplacement: opts.DefaultReplacement,
	}

	seen := make(map[string]struct{}, len(rules))
	words := make([]string, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.Word) == "" {
			return nil, fmt.Errorf("textguard: 空模式被拒绝")
		}
		key := m.keyRunes(rule.Word)
		if len(key) == 0 {
			return nil, fmt.Errorf("textguard: 模式 %q 归一化后为空", rule.Word)
		}
		if len(key) > maxRunes {
			return nil, fmt.Errorf("textguard: 模式 %q 长度 %d 超过上限 %d", rule.Word, len(key), maxRunes)
		}
		k := string(key)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		m.main.insert(key, acOutput{word: rule.Word, replacement: rule.Replacement, runes: len(key)})
		words = append(words, rule.Word)
	}

	wseen := make(map[string]struct{}, len(opts.Whitelist))
	for _, w := range opts.Whitelist {
		if strings.TrimSpace(w) == "" {
			return nil, fmt.Errorf("textguard: 白名单含空模式")
		}
		key := m.keyRunes(w)
		if len(key) == 0 {
			return nil, fmt.Errorf("textguard: 白名单 %q 归一化后为空", w)
		}
		if len(key) > maxRunes {
			return nil, fmt.Errorf("textguard: 白名单 %q 长度 %d 超过上限 %d", w, len(key), maxRunes)
		}
		k := string(key)
		if _, ok := wseen[k]; ok {
			continue
		}
		wseen[k] = struct{}{}
		m.white.insert(key, acOutput{word: w, runes: len(key)})
	}

	m.main.build()
	m.white.build()
	sort.Strings(words)
	m.words = words
	return m, nil
}

// normText 是查询侧的归一化结果，同时保留到原文 rune 下标的映射。
type normText struct {
	runes     []rune
	origStart []int // 归一化 rune i 对应原文起始 rune 下标
	origEnd   []int // 归一化 rune i 对应原文结束 rune 下标（不含）
}

// normalize 在查询侧做归一化，不修改原文。
func (m *Matcher) normalizeText(s string, withMap bool) normText {
	src := []rune(s)
	nt := normText{runes: make([]rune, 0, len(src))}
	if withMap {
		nt.origStart = make([]int, 0, len(src))
		nt.origEnd = make([]int, 0, len(src))
	}
	add := func(r rune, start, end int) {
		nt.runes = append(nt.runes, r)
		if withMap {
			nt.origStart = append(nt.origStart, start)
			nt.origEnd = append(nt.origEnd, end)
		}
	}
	for i := 0; i < len(src); {
		// 反斜杠 + u + 4 位十六进制。
		if m.normalize && src[i] == '\\' && i+1 < len(src) && src[i+1] == 'u' && i+5 < len(src) {
			if v, ok := parseHexRunes(src[i+2 : i+6]); ok {
				add(m.foldRune(v), i, i+6)
				i += 6
				continue
			}
		}
		// 十六进制实体转义。
		if m.normalize && src[i] == '&' && i+2 < len(src) && src[i+1] == '#' && (src[i+2] == 'x' || src[i+2] == 'X') {
			j := i + 3
			for j < len(src) && isHexRune(src[j]) {
				j++
			}
			if j > i+3 && j < len(src) && src[j] == ';' {
				if v, ok := parseHexRunes(src[i+3 : j]); ok {
					add(m.foldRune(v), i, j+1)
					i = j + 1
					continue
				}
			}
		}
		add(m.foldRune(src[i]), i, i+1)
		i++
	}
	return nt
}

// keyRunes 返回模式归一化后的 rune 序列（构建期用，无需下标映射）。
func (m *Matcher) keyRunes(s string) []rune {
	return m.normalizeText(s, false).runes
}

// foldRune 依次应用全角/半角归并与 ASCII 大小写折叠。
func (m *Matcher) foldRune(r rune) rune {
	if m.normalize {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
	}
	if m.caseInsensitive && r >= 'A' && r <= 'Z' {
		r += 'a' - 'A'
	}
	return r
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func hexVal(r rune) (int64, bool) {
	switch {
	case r >= '0' && r <= '9':
		return int64(r - '0'), true
	case r >= 'a' && r <= 'f':
		return int64(r-'a') + 10, true
	case r >= 'A' && r <= 'F':
		return int64(r-'A') + 10, true
	}
	return 0, false
}

func parseHexRunes(rs []rune) (rune, bool) {
	if len(rs) == 0 || len(rs) > 6 {
		return 0, false
	}
	var v int64
	for _, r := range rs {
		d, ok := hexVal(r)
		if !ok {
			return 0, false
		}
		v = v<<4 | d
	}
	if v > 0x10FFFF {
		return 0, false
	}
	return rune(v), true
}

// Find 返回 text 中所有原始命中（含重叠，不受白名单影响），按 Start 升序、同起点长的在前。
// 主要用于诊断；替换请用 Replacements/Replace。
func (m *Matcher) Find(text string) []Match {
	if m == nil {
		return nil
	}
	nt := m.normalizeText(text, true)
	hits := m.main.scan(nt.runes)
	if len(hits) == 0 {
		return nil
	}
	out := make([]Match, 0, len(hits))
	for _, h := range hits {
		out = append(out, Match{
			Word:  h.out.word,
			Start: nt.origStart[h.start],
			End:   nt.origEnd[h.end-1],
		})
	}
	sortMatches(out)
	return out
}

func sortMatches(out []Match) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].End != out[j].End {
			return out[i].End > out[j].End
		}
		return out[i].Word < out[j].Word
	})
}

// candidate 是消解前的候选替换。
type candidate struct {
	start, end  int
	word        string
	replacement string
}

func sortCandidates(cands []candidate) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].start != cands[j].start {
			return cands[i].start < cands[j].start
		}
		if cands[i].end != cands[j].end {
			return cands[i].end > cands[j].end
		}
		return cands[i].word < cands[j].word
	})
}

// Replacements 返回重叠消解 + 白名单剔除后的替换列表，按 Start 升序、互不重叠。
func (m *Matcher) Replacements(text string) []Replacement {
	if m == nil {
		return nil
	}
	nt := m.normalizeText(text, true)
	hits := m.main.scan(nt.runes)
	if len(hits) == 0 {
		return nil
	}
	cands := make([]candidate, 0, len(hits))
	for _, h := range hits {
		start := nt.origStart[h.start]
		end := nt.origEnd[h.end-1]
		cands = append(cands, candidate{
			start:       start,
			end:         end,
			word:        h.out.word,
			replacement: m.effectiveReplacement(h.out, end-start),
		})
	}
	sortCandidates(cands)

	white := m.whiteRanges(nt)
	out := make([]Replacement, 0, len(cands))
	lastEnd := 0
	for _, c := range cands {
		if c.start < lastEnd { // 与已选区间重叠，跳过
			continue
		}
		if overlapsAny(c.start, c.end, white) { // 落在白名单区间内，剔除
			continue
		}
		out = append(out, Replacement{Start: c.start, End: c.end, Replacement: c.replacement})
		lastEnd = c.end
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Matcher) effectiveReplacement(out acOutput, origLen int) string {
	if out.replacement != "" {
		return out.replacement
	}
	if m.defaultReplacement != "" {
		return m.defaultReplacement
	}
	return strings.Repeat("*", origLen)
}

// whiteRanges 把白名单命中映射回原文 rune 区间。
func (m *Matcher) whiteRanges(nt normText) [][2]int {
	hits := m.white.scan(nt.runes)
	if len(hits) == 0 {
		return nil
	}
	out := make([][2]int, 0, len(hits))
	for _, h := range hits {
		out = append(out, [2]int{nt.origStart[h.start], nt.origEnd[h.end-1]})
	}
	return out
}

func overlapsAny(start, end int, ranges [][2]int) bool {
	for _, r := range ranges {
		if start < r[1] && r[0] < end {
			return true
		}
	}
	return false
}

// Replace 从后往前一次性重建字符串，避免前面的替换破坏后面的下标。
func (m *Matcher) Replace(text string) string {
	reps := m.Replacements(text)
	if len(reps) == 0 {
		return text
	}
	runes := []rune(text)
	for i := len(reps) - 1; i >= 0; i-- {
		r := reps[i]
		rep := []rune(r.Replacement)
		merged := make([]rune, 0, len(runes)-(r.End-r.Start)+len(rep))
		merged = append(merged, runes[:r.Start]...)
		merged = append(merged, rep...)
		merged = append(merged, runes[r.End:]...)
		runes = merged
	}
	return string(runes)
}

// Has 判断 text 在重叠消解与白名单剔除后是否仍会产生替换。
func (m *Matcher) Has(text string) bool {
	return len(m.Replacements(text)) > 0
}

// Words 返回已编译的词表：去重、稳定排序的一份拷贝。
func (m *Matcher) Words() []string {
	if m == nil {
		return nil
	}
	out := make([]string, len(m.words))
	copy(out, m.words)
	return out
}

// Engine 用原子指针持有一个不可变 Matcher，实现不阻塞查询的热加载。
type Engine struct {
	cur atomic.Pointer[Matcher]
}

// NewEngine 用初始 Matcher 构造 Engine；m 可为 nil（此时全部查询视为无命中）。
func NewEngine(m *Matcher) *Engine {
	e := &Engine{}
	e.cur.Store(m)
	return e
}

// Load 返回当前 Matcher；并发 Swap 期间是某一份完整快照，绝不为半成品。
func (e *Engine) Load() *Matcher {
	if e == nil {
		return nil
	}
	return e.cur.Load()
}

// Swap 原子替换当前 Matcher，不阻塞正在进行的查询。
func (e *Engine) Swap(m *Matcher) {
	if e == nil {
		return
	}
	e.cur.Store(m)
}

// Find 委托当前 Matcher。
func (e *Engine) Find(text string) []Match { return e.Load().Find(text) }

// Has 委托当前 Matcher。
func (e *Engine) Has(text string) bool { return e.Load().Has(text) }

// Replacements 委托当前 Matcher。
func (e *Engine) Replacements(text string) []Replacement { return e.Load().Replacements(text) }

// Replace 委托当前 Matcher。
func (e *Engine) Replace(text string) string { return e.Load().Replace(text) }

// Words 委托当前 Matcher。
func (e *Engine) Words() []string { return e.Load().Words() }
