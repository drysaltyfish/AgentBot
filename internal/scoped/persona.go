package scoped

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yml
var builtinPersonas embed.FS

const (
	builtinPersonasDir = "builtin"
	personaExtYML      = ".yml"
	personaExtYAML     = ".yaml"
)

// Persona 是一份人格定义。
type Persona struct {
	// Name 是人格名，必须与文件名一致。
	Name string
	// Description 是给人看的一句话说明。
	Description string
	// SystemPrompt 是人格的 system prompt 正文（prompt_file 已在此展开）。
	SystemPrompt string
	// Config 是该人格的作用域配置覆盖。
	Config map[string]any
	// Dir / File 记录来源，出错时报给用户。
	Dir  string
	File string
}

// Scope 返回该人格配置应写入的作用域。
func (p Persona) Scope() string { return PersonaScope(p.Name) }

// Fingerprint 返回人格设定的短摘要，供 F-65 半静态段哈希使用。
func (p Persona) Fingerprint() string {
	return shortHash(p.Name + "\x00" + p.SystemPrompt)
}

// personaFile 是人格 YAML 的线格式。
type personaFile struct {
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description"`
	SystemPrompt string         `yaml:"system_prompt"`
	PromptFile   string         `yaml:"prompt_file"`
	Config       map[string]any `yaml:"config"`
}

// rawPersona 是尚未解析的一份人格文件。
type rawPersona struct {
	path string // 展示给用户的路径
	dir  string // prompt_file 的相对目录
	data []byte
}

// Registry 是已加载并校验通过的人格集合。
type Registry struct {
	mu       sync.RWMutex
	personas map[string]Persona
}

// Load 加载内置人格，再用 dir 下的外部同名文件覆盖。
//
// dir 下的重名、非法名、缺字段、解析失败都会一次性报错并指出文件
// （F-25 fail-fast）；dir 为空或不存在时只用内置版本。
func Load(dir string) (*Registry, error) {
	r := &Registry{personas: map[string]Persona{}}
	if err := r.loadEmbedded(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dir) != "" {
		if err := r.loadDir(dir); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// NewRegistry 构造空注册表（测试用；正常路径用 Load）。
func NewRegistry() *Registry { return &Registry{personas: map[string]Persona{}} }

func (r *Registry) loadEmbedded() error {
	entries, err := fs.ReadDir(builtinPersonas, builtinPersonasDir)
	if err != nil {
		return fmt.Errorf("scoped: 读取内置人格目录: %w", err)
	}
	batch := make([]rawPersona, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), personaExtYML) {
			continue
		}
		data, rerr := builtinPersonas.ReadFile(builtinPersonasDir + "/" + e.Name())
		if rerr != nil {
			return fmt.Errorf("scoped: 读取内置人格 %s: %w", e.Name(), rerr)
		}
		batch = append(batch, rawPersona{path: e.Name(), dir: builtinPersonasDir, data: data})
	}
	return r.mergeBatch(batch, false, "内置人格")
}

func (r *Registry) loadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("scoped: 读取人格目录 %s: %w", dir, err)
	}
	batch := make([]rawPersona, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != personaExtYML && ext != personaExtYAML {
			continue
		}
		full := filepath.Join(dir, e.Name())
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			return fmt.Errorf("scoped: 读取人格文件 %s: %w", full, rerr)
		}
		batch = append(batch, rawPersona{path: full, dir: dir, data: data})
	}
	return r.mergeBatch(batch, true, "人格目录 "+dir)
}

// mergeBatch 解析一批人格文件，报告批内重名与全部字段问题，然后合并进注册表。
// override 为 false 时不覆盖已有同名（内置优先）；为 true 时外部同名覆盖内置。
func (r *Registry) mergeBatch(batch []rawPersona, override bool, source string) error {
	seen := make(map[string]string, len(batch))
	local := make(map[string]Persona, len(batch))
	problems := make([]string, 0)
	for _, rp := range batch {
		p, err := parsePersona(rp)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if prev, ok := seen[p.Name]; ok {
			problems = append(problems, fmt.Sprintf("%s 与 %s 都定义人格 %q", rp.path, prev, p.Name))
			continue
		}
		seen[p.Name] = rp.path
		local[p.Name] = p
	}
	if len(problems) > 0 {
		return fmt.Errorf("scoped: %s 校验失败:\n  - %s", source, strings.Join(problems, "\n  - "))
	}
	for name, p := range local {
		if _, exists := r.personas[name]; exists && !override {
			continue
		}
		r.personas[name] = p
	}
	return nil
}

func parsePersona(rp rawPersona) (Persona, error) {
	dec := yaml.NewDecoder(bytes.NewReader(rp.data))
	dec.KnownFields(true)
	var pf personaFile
	if err := dec.Decode(&pf); err != nil {
		return Persona{}, fmt.Errorf("%s: 解析失败: %w", rp.path, err)
	}
	stem := strings.TrimSuffix(filepath.Base(rp.path), filepath.Ext(rp.path))
	name := strings.TrimSpace(pf.Name)

	problems := make([]string, 0, 3)
	switch {
	case name == "":
		problems = append(problems, "缺少必填字段 name")
	case name != stem:
		problems = append(problems, fmt.Sprintf("name %q 与文件名 %q 不一致", name, stem))
	}
	if !ValidPersonaName(stem) {
		problems = append(problems, fmt.Sprintf("非法人格名 %q（只允许小写字母、数字、'.'、'_'、'-'，且以字母或数字开头）", stem))
	}
	systemPrompt := pf.SystemPrompt
	switch {
	case strings.TrimSpace(systemPrompt) == "" && strings.TrimSpace(pf.PromptFile) == "":
		problems = append(problems, "缺少必填字段 system_prompt 或 prompt_file")
	case strings.TrimSpace(systemPrompt) != "" && strings.TrimSpace(pf.PromptFile) != "":
		problems = append(problems, "system_prompt 与 prompt_file 不能同时设置")
	}
	if len(problems) > 0 {
		return Persona{}, fmt.Errorf("%s: %s", rp.path, strings.Join(problems, "；"))
	}

	if strings.TrimSpace(pf.PromptFile) != "" {
		path := pf.PromptFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(rp.dir, path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return Persona{}, fmt.Errorf("%s: 读取 prompt_file %s 失败: %w", rp.path, path, err)
		}
		systemPrompt = string(raw)
		if strings.TrimSpace(systemPrompt) == "" {
			return Persona{}, fmt.Errorf("%s: prompt_file %s 内容为空", rp.path, path)
		}
	}

	cfg := pf.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return Persona{
		Name:         stem,
		Description:  strings.TrimSpace(pf.Description),
		SystemPrompt: systemPrompt,
		Config:       cfg,
		Dir:          rp.dir,
		File:         rp.path,
	}, nil
}

// Names 返回全部人格名，按字典序排列。
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.personas))
	for name := range r.personas {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Get 返回人格定义。
func (r *Registry) Get(name string) (Persona, bool) {
	if r == nil {
		return Persona{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.personas[NormalizePersona(name)]
	return p, ok
}

// Has 报告人格是否已定义。
func (r *Registry) Has(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.personas[NormalizePersona(name)]
	return ok
}

// Len 返回人格数量。
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.personas)
}

// ValidateRefs 校验一组被引用的人格名都存在；缺失时一次性报告（F-25 fail-fast）。
func (r *Registry) ValidateRefs(refs ...string) error {
	problems := make([]string, 0)
	for _, ref := range refs {
		name := strings.TrimSpace(ref)
		if name == "" {
			continue
		}
		if !ValidPersonaName(name) {
			problems = append(problems, fmt.Sprintf("%q 不是合法人格名", ref))
			continue
		}
		if !r.Has(name) {
			problems = append(problems, fmt.Sprintf("%q 未定义", name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("scoped: 引用了不存在的人格: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Apply 把每个人格定义里的 config 覆盖写入 cfg 对应的人格作用域。
func (r *Registry) Apply(c *Config) {
	if c == nil {
		return
	}
	for _, name := range r.Names() {
		p, ok := r.Get(name)
		if !ok {
			continue
		}
		c.Merge(p.Scope(), p.Config)
	}
}

// Swap 用一份**已加载并校验过**的注册表替换当前内容（F-24 热加载）。
//
// 与 Reload 的分工：Reload 自己去读目录、失败时自行告警；Swap 只接受已经
// 通过校验的结果——校验发生在 watcher 的 load 里，失败时 watcher 根本不会
// 调用 Swap，因此这里可以无条件替换。
func (r *Registry) Swap(next *Registry) {
	if r == nil || next == nil {
		return
	}
	next.mu.RLock()
	personas := make(map[string]Persona, len(next.personas))
	for name, p := range next.personas {
		personas[name] = p
	}
	next.mu.RUnlock()

	r.mu.Lock()
	r.personas = personas
	r.mu.Unlock()
}

// Reload 重新加载外部目录；失败时保留现有注册表并调用 warn，不返回错误（F-24）。
func (r *Registry) Reload(dir string, warn func(string)) {
	if warn == nil {
		warn = func(string) {}
	}
	fresh, err := Load(dir)
	if err != nil {
		warn("人格覆盖文件解析失败，保留旧版本: " + err.Error())
		return
	}
	r.mu.Lock()
	r.personas = fresh.personas
	r.mu.Unlock()
}
