// Package prompt 实现提示词模板引擎（FEATURES.md F-33）。
//
// 硬性要求：命名占位（不用位置参数）、渲染前统一行尾、启动期检出语法与变量错误。
package prompt

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
	"time"
)

//go:embed templates/*.tmpl
var builtin embed.FS

// builtinPrefix 是 embed 里的目录前缀。
const builtinPrefix = "templates/"

// Options 配置引擎。
type Options struct {
	// Dir 是外部覆盖目录；为空表示只用内置模板。
	Dir string
	// SampleData 是启动期预渲染用的样例数据。
	SampleData map[string]any
	// Now 注入时间源。
	Now func() time.Time
	// Timeout 是单次渲染的告警阈值（默认 50ms）。
	Timeout time.Duration
	// OnSlowRender 在单次渲染超过 Timeout 时被调用（超时只告警，不失败）。
	OnSlowRender func(name string, took time.Duration)
	// Measure 供测试注入，默认 time.Since；避免依赖真实时钟粒度。
	Measure func(start time.Time) time.Duration
}

// Engine 是模板引擎。
type Engine struct {
	mu     sync.Mutex
	opts   Options
	cache  map[string]cacheEntry
	names  []string
	loaded map[string]*template.Template
}

type cacheEntry struct {
	hash string
	text string
}

// New 构造引擎并加载模板（不做校验，校验用 ValidateStartup）。
func New(opts Options) *Engine {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SampleData == nil {
		opts.SampleData = map[string]any{}
	}
	e := &Engine{opts: opts, cache: map[string]cacheEntry{}, loaded: map[string]*template.Template{}}
	return e
}

// Normalize 统一行尾：把 CRLF 归一为 LF（F-33 硬性要求）。
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func (e *Engine) funcs() template.FuncMap {
	return template.FuncMap{
		"now": func() time.Time { return e.opts.Now() },
		"timezone": func(tz string, t time.Time) (string, error) {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return "", fmt.Errorf("unknown timezone %q: %w", tz, err)
			}
			return t.In(loc).Format(time.RFC3339), nil
		},
		"join":  strings.Join,
		"quote": func(s string) string { return "「" + s + "」" },
		"mdTable": func(headers []string, rows [][]string) string {
			var b strings.Builder
			b.WriteString("| " + strings.Join(headers, " | ") + " |\n")
			b.WriteString("|")
			for range headers {
				b.WriteString("---|")
			}
			b.WriteString("\n")
			for _, row := range rows {
				cells := make([]string, len(headers))
				for i := range headers {
					if i < len(row) {
						cells[i] = row[i]
					}
				}
				b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
			}
			return strings.TrimRight(b.String(), "\n")
		},
	}
}

// Names 返回可用模板名（不含扩展名）。
func (e *Engine) Names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.discoverLocked()
	out := make([]string, len(e.names))
	copy(out, e.names)
	return out
}

func (e *Engine) discoverLocked() {
	if e.names != nil {
		return
	}
	seen := map[string]struct{}{}
	if entries, err := builtin.ReadDir("templates"); err == nil {
		for _, entry := range entries {
			name := strings.TrimSuffix(entry.Name(), ".tmpl")
			seen[name] = struct{}{}
		}
	}
	if e.opts.Dir != "" {
		if entries, err := os.ReadDir(e.opts.Dir); err == nil {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tmpl") {
					seen[strings.TrimSuffix(entry.Name(), ".tmpl")] = struct{}{}
				}
			}
		}
	}
	for name := range seen {
		e.names = append(e.names, name)
	}
	sort.Strings(e.names)
}

// source 返回模板文本：外部同名文件优先，缺失时回退内置版本。
func (e *Engine) source(name string) (string, bool, error) {
	if e.opts.Dir != "" {
		p := filepath.Join(e.opts.Dir, name+".tmpl")
		raw, err := os.ReadFile(p)
		if err == nil {
			return Normalize(string(raw)), true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("read template %s: %w", p, err)
		}
	}
	raw, err := builtin.ReadFile(builtinPrefix + name + ".tmpl")
	if err != nil {
		return "", false, fmt.Errorf("template %q not found", name)
	}
	return Normalize(string(raw)), false, nil
}

func (e *Engine) parse(name string) (*template.Template, error) {
	e.mu.Lock()
	if t, ok := e.loaded[name]; ok {
		e.mu.Unlock()
		return t, nil
	}
	e.mu.Unlock()

	src, _, err := e.source(name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Funcs(e.funcs()).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	e.mu.Lock()
	e.loaded[name] = t
	e.mu.Unlock()
	return t, nil
}

// ValidateStartup 用样例数据预渲染全部模板，把错误提前到启动期。
//
// 语法错误带行号；引用了样例数据里不存在的变量也会在这里被指出行号。
func (e *Engine) ValidateStartup() error {
	names := e.Names()
	if len(names) == 0 {
		return fmt.Errorf("no prompt templates found")
	}
	e.mu.Lock()
	e.discoverLocked()
	e.mu.Unlock()

	var errs []string
	for _, name := range names {
		t, err := e.parse(name)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		src, _, serr := e.source(name)
		if serr != nil {
			errs = append(errs, serr.Error())
			continue
		}
		if err := checkFields(name, t, src, e.opts.SampleData); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if _, err := e.Render(name, e.opts.SampleData); err != nil {
			errs = append(errs, fmt.Sprintf("render %s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("prompt validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// checkFields 走一遍语法树，报告引用了样例数据中不存在的变量（带行号）。
// lineOf 把 parse.Pos（字节偏移）换算成行号。
func lineOf(src string, pos parse.Pos) int {
	off := int(pos)
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	return 1 + strings.Count(src[:off], "\n")
}

func checkFields(name string, t *template.Template, src string, sample map[string]any) error {
	var problems []string
	for _, tt := range t.Templates() {
		if tt.Tree == nil || tt.Tree.Root == nil {
			continue
		}
		var walk func(parse.Node)
		walk = func(n parse.Node) {
			switch node := n.(type) {
			case *parse.ListNode:
				if node == nil {
					return
				}
				for _, c := range node.Nodes {
					walk(c)
				}
			case *parse.ActionNode:
				walk(node.Pipe)
			case *parse.PipeNode:
				if node == nil {
					return
				}
				for _, c := range node.Cmds {
					walk(c)
				}
			case *parse.CommandNode:
				if node == nil {
					return
				}
				for _, a := range node.Args {
					walk(a)
				}
			case *parse.FieldNode:
				if len(node.Ident) == 0 {
					return
				}
				if _, ok := sample[node.Ident[0]]; !ok {
					problems = append(problems, fmt.Sprintf("%s:%d: 未知变量 .%s", tt.Name(), lineOf(src, node.Position()), strings.Join(node.Ident, ".")))
				}
			case *parse.ChainNode:
				walk(node.Node)
			case *parse.IfNode:
				walk(node.Pipe)
				walk(node.List)
				walk(node.ElseList)
			case *parse.RangeNode:
				walk(node.Pipe)
				walk(node.List)
				walk(node.ElseList)
			case *parse.WithNode:
				walk(node.Pipe)
				walk(node.List)
				walk(node.ElseList)
			case *parse.TemplateNode:
				walk(node.Pipe)
			}
		}
		walk(tt.Tree.Root)
	}
	if len(problems) > 0 {
		return fmt.Errorf("template %s: %s", name, strings.Join(problems, "; "))
	}
	return nil
}

// Render 渲染模板；结果按 (模板名, 数据指纹) 缓存。
func (e *Engine) Render(name string, data any) (string, error) {
	t, err := e.parse(name)
	if err != nil {
		return "", err
	}
	key := cacheKey(name, data)
	e.mu.Lock()
	if entry, ok := e.cache[key]; ok {
		e.mu.Unlock()
		return entry.text, nil
	}
	e.mu.Unlock()

	start := time.Now()
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %s: %w", name, err)
	}
	out := Normalize(buf.String())
	if limit := e.opts.Timeout; limit > 0 && e.opts.OnSlowRender != nil {
		measure := e.opts.Measure
		if measure == nil {
			measure = time.Since
		}
		if took := measure(start); took > limit {
			e.opts.OnSlowRender(name, took)
		}
	}
	e.mu.Lock()
	e.cache[key] = cacheEntry{hash: hashOf(out), text: out}
	e.mu.Unlock()
	return out, nil
}

// RenderWithHash 返回渲染结果与其哈希（供 F-65 的段哈希使用）。
func (e *Engine) RenderWithHash(name string, data any) (string, string, error) {
	out, err := e.Render(name, data)
	if err != nil {
		return "", "", err
	}
	return out, hashOf(out), nil
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func cacheKey(name string, data any) string {
	return name + "\x00" + fmt.Sprintf("%v", data)
}

// Invalidate 清空渲染缓存（模板热加载时调用）。
func (e *Engine) Invalidate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = map[string]cacheEntry{}
	e.loaded = map[string]*template.Template{}
	e.names = nil
}
