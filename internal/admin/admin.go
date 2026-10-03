// Package admin 实现管理命令框架与可注入的内置命令（FEATURES.md F-71）。
//
// 设计目标：消息入口与本地 CLI 复用同一份命令实现，因此本包对"命令从哪来"
// 一无所知——调用方把命令文本、调用者身份与来源（Source）交进来，拿到一段
// 回复文本即可自行发送；消息入口只是优先级更高，实现完全相同。
//
// 依赖全部通过 Options 注入：
//
//   - Checker 判定调用者是否有权限（本包不关心超管名单从哪来）；
//   - Auditor 记录每一次调用（成功、拒绝、失败都记，F-60）；
//   - Filter 把回复送进出口过滤链（F-55）；
//   - 各内置命令的数据源（Status/Routes/Cost/Reload/PromptHash）由调用方提供。
//
// 因此本包不 import internal/router、internal/audit、internal/toggle 等任何
// 业务包：/ban、/switch 这类子系统各自特有的命令由调用方通过 Register 自行
// 注册，框架只提供解析、鉴权、审计、分发与输出限长。
package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultMaxReply 是回复的最大 rune 数；超出时截断并附截断标记。
const DefaultMaxReply = 2000

// 框架级错误。调用方用 errors.Is 判定。
var (
	// ErrUnknownCommand 表示输入没有匹配到任何已注册命令。
	ErrUnknownCommand = errors.New("admin: unknown command")
	// ErrUsage 表示命令的参数不符合用法。
	ErrUsage = errors.New("admin: invalid arguments")
)

// Source 标识命令入口。消息入口优先，但两条入口复用同一次 Dispatch。
type Source string

// 支持的入口来源。
const (
	SourceMessage Source = "message"
	SourceCLI     Source = "cli"
)

// DenyMode 决定未授权调用如何回应；两种模式下都一定记审计。
type DenyMode int

// 未授权时的处理方式。
const (
	// DenySilent 静默忽略：回复空串，不泄露命令是否存在。
	DenySilent DenyMode = iota
	// DenyExplicit 明确拒绝：回复"无权限"。
	DenyExplicit
)

// Request 是一次管理命令调用的原始输入。
type Request struct {
	// Text 是原始命令文本，可带或不带前导 '/'。
	Text string
	// UserID 与 GroupID 是调用者身份，供 Checker 判定权限。
	UserID  int64
	GroupID int64
	// Source 是入口来源，仅用于审计与展示。
	Source Source
}

// Invocation 是解析后的调用：在 Request 之上补齐命令名与参数。
type Invocation struct {
	Request
	// Command 是匹配到的命令名（已小写化），未匹配时为输入的首个词。
	Command string
	// Args 是命令名之后的参数（保留原始大小写）。
	Args []string
}

// Handler 执行一条命令并返回回复文本。err 非空表示执行失败。
type Handler func(ctx context.Context, inv Invocation) (string, error)

// Checker 判定调用者是否有权执行该命令；返回 false 即拒绝。nil 表示全部拒绝。
type Checker func(ctx context.Context, inv Invocation) bool

// AuditEvent 是一次调用的审计记录。At/Duration 由框架填充，其余为原样信息。
type AuditEvent struct {
	At         time.Time
	Command    string
	UserID     int64
	GroupID    int64
	Source     Source
	Authorized bool
	Result     string // "ok" | "denied" | "error"
	Error      string
	Duration   time.Duration
}

// Auditor 接收审计记录；由调用方适配 internal/audit（F-60）。nil 表示不记录。
type Auditor func(AuditEvent)

// Options 描述 Module 的构造参数。
type Options struct {
	// Checker 是必需的鉴权函数；nil 视为拒绝一切调用（fail-closed）。
	Checker Checker
	// Auditor 记录每一次调用；nil 表示不记录（生产环境不应为 nil）。
	Auditor Auditor
	// Filter 在回复发出前做出口过滤（F-55）；nil 表示恒等。
	Filter func(string) string
	// MaxReply 限制回复的 rune 数；<=0 取 DefaultMaxReply。
	MaxReply int
	// DenyMode 决定未授权调用是静默忽略还是明确拒绝。
	DenyMode DenyMode
	// Now 提供时间源；nil 表示 time.Now。
	Now func() time.Time
}

// Command 是一条已注册命令的公开描述，供 /help 展示。
type Command struct {
	Name string
	Help string
}

type entry struct {
	help    string
	handler Handler
}

// Module 是管理命令的注册表与分发器，可并发使用。
type Module struct {
	opts Options
	now  func() time.Time

	mu   sync.RWMutex
	cmds map[string]entry
}

// New 构造 Module 并注册内置的 /help。
func New(opts Options) *Module {
	if opts.MaxReply <= 0 {
		opts.MaxReply = DefaultMaxReply
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	m := &Module{opts: opts, now: now, cmds: make(map[string]entry)}
	// 固定名 + 非 nil handler，Register 不会失败。
	_ = m.Register("help", "列出全部管理命令", m.helpHandler())
	return m
}

// Register 注册或覆盖一条命令。name 可含空格（多词命令走最长前缀匹配）；
// 解析时大小写不敏感。name 为空或 handler 为 nil 时返回错误。
func (m *Module) Register(name, help string, h Handler) error {
	key := normalizeName(name)
	if key == "" {
		return fmt.Errorf("admin: register %q: empty command name", name)
	}
	if h == nil {
		return fmt.Errorf("admin: register %q: nil handler", key)
	}
	m.mu.Lock()
	m.cmds[key] = entry{help: help, handler: h}
	m.mu.Unlock()
	return nil
}

// Commands 返回已注册命令的描述，按名称升序。
func (m *Module) Commands() []Command {
	m.mu.RLock()
	out := make([]Command, 0, len(m.cmds))
	for name, e := range m.cmds {
		out = append(out, Command{Name: name, Help: e.help})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Dispatch 解析并执行一次调用，返回回复文本。
//
// 顺序固定：解析 → 鉴权 → 存在性 → 执行。鉴权先于"命令是否存在"，
// 未授权的调用者无法通过错误信息探测命令表。无论结果如何都会记审计。
func (m *Module) Dispatch(ctx context.Context, req Request) (string, error) {
	start := m.now()
	name, args, known := m.resolve(req.Text)
	inv := Invocation{Request: req, Command: name, Args: args}

	if !m.authorized(ctx, inv) {
		m.audit(inv, false, "denied", nil, m.now().Sub(start))
		if m.opts.DenyMode == DenyExplicit {
			return "无权限执行该命令。", nil
		}
		return "", nil
	}

	h, ok := m.lookup(inv.Command)
	if !ok || !known {
		m.audit(inv, true, "error", ErrUnknownCommand, m.now().Sub(start))
		return "", fmt.Errorf("%w: %q", ErrUnknownCommand, inv.Command)
	}

	reply, err := h(ctx, inv)
	if err != nil {
		m.audit(inv, true, "error", err, m.now().Sub(start))
		return reply, fmt.Errorf("admin: command %q: %w", inv.Command, err)
	}
	reply = m.finish(reply)
	m.audit(inv, true, "ok", nil, m.now().Sub(start))
	return reply, nil
}

// resolve 找到最长匹配的已注册命令名，并返回其余参数。
//
// 多词命令（如 "config reload"）因此优先于其单词前缀；输入首词大小写不敏感，
// 参数保留原始大小写。
func (m *Module) resolve(text string) (string, []string, bool) {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "/")
	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		return "", nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(tokens); i >= 1; i-- {
		candidate := strings.ToLower(strings.Join(tokens[:i], " "))
		if _, ok := m.cmds[candidate]; ok {
			args := append([]string(nil), tokens[i:]...)
			return candidate, args, true
		}
	}
	return strings.ToLower(tokens[0]), append([]string(nil), tokens[1:]...), false
}

// lookup 返回命令处理器；不存在时 ok 为 false。
func (m *Module) lookup(name string) (Handler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.cmds[name]
	if !ok {
		return nil, false
	}
	return e.handler, true
}

// authorized 执行注入的鉴权；Checker 为 nil 时一律拒绝（fail-closed）。
func (m *Module) authorized(ctx context.Context, inv Invocation) bool {
	if m.opts.Checker == nil {
		return false
	}
	return m.opts.Checker(ctx, inv)
}

// audit 组装并投递审计记录；Auditor 为 nil 时直接返回。
func (m *Module) audit(inv Invocation, authorized bool, result string, cause error, took time.Duration) {
	if m.opts.Auditor == nil {
		return
	}
	ev := AuditEvent{
		At:         m.now(),
		Command:    inv.Command,
		UserID:     inv.UserID,
		GroupID:    inv.GroupID,
		Source:     inv.Source,
		Authorized: authorized,
		Result:     result,
		Duration:   took,
	}
	if cause != nil {
		ev.Error = cause.Error()
	}
	m.opts.Auditor(ev)
}

// finish 对回复套用出口过滤与长度限制。
func (m *Module) finish(reply string) string {
	if m.opts.Filter != nil {
		reply = m.opts.Filter(reply)
	}
	return truncate(reply, m.opts.MaxReply)
}

// truncate 把 s 截到 limit 个 rune；截断时以标记结尾。
func truncate(s string, limit int) string {
	if limit <= 0 {
		limit = DefaultMaxReply
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	const marker = "…(已截断)"
	markerLen := len([]rune(marker))
	if limit > len(runes) {
		limit = len(runes)
	}
	if limit <= markerLen {
		return string(runes[:limit])
	}
	return string(runes[:limit-markerLen]) + marker
}

// normalizeName 归一化命令名：去空白、小写、去掉前导 '/'。
func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// helpHandler 返回 /help 的处理器：列出全部命令。
func (m *Module) helpHandler() Handler {
	return func(_ context.Context, _ Invocation) (string, error) {
		cmds := m.Commands()
		var b strings.Builder
		b.WriteString("可用管理命令：\n")
		for _, c := range cmds {
			fmt.Fprintf(&b, "- /%s：%s\n", c.Name, c.Help)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
}

// Builtins 汇集可注入的内置命令数据源；为 nil 的命令不会被注册。
type Builtins struct {
	// Status 提供 /status 的运行时长、事件数、活跃会话、队列深度与错误率。
	Status StatusFunc
	// Routes 提供 /routes 的路由快照。
	Routes RoutesFunc
	// Cost 提供 /cost today|session 的成本统计（F-66）。
	Cost CostFunc
	// Reload 提供 /config reload 的手动热加载（F-24）。
	Reload ReloadFunc
	// PromptHash 提供 /prompt-hash 的提示词分段哈希（F-65）。
	PromptHash PromptHashFunc
}

// RegisterBuiltins 按 Builtins 中非 nil 的数据源注册对应内置命令。
//
// /ban、/unban、/banlist（F-58）与 /switch（F-19）依赖各自子系统，
// 不在此硬编码，由调用方用 Register 自行注册。
func (m *Module) RegisterBuiltins(b Builtins) error {
	if b.Status != nil {
		if err := m.Register("status", "查看运行状态", statusHandler(b.Status)); err != nil {
			return err
		}
	}
	if b.Routes != nil {
		if err := m.Register("routes", "列出已注册路由", routesHandler(b.Routes)); err != nil {
			return err
		}
	}
	if b.Cost != nil {
		if err := m.Register("cost", "查看成本统计：/cost today|session", costHandler(b.Cost)); err != nil {
			return err
		}
	}
	if b.Reload != nil {
		if err := m.Register("config", "热加载配置：/config reload", configHandler(b.Reload)); err != nil {
			return err
		}
	}
	if b.PromptHash != nil {
		if err := m.Register("prompt-hash", "打印提示词各段哈希", promptHashHandler(b.PromptHash)); err != nil {
			return err
		}
	}
	return nil
}

// Status 是 /status 展示的运行快照。
type Status struct {
	Uptime         time.Duration
	Events         uint64
	ActiveSessions int
	QueueDepth     int
	// ErrorRate 是 [0,1] 的错误比例。
	ErrorRate float64
}

// StatusFunc 采集运行快照。
type StatusFunc func(ctx context.Context) (Status, error)

// RouteInfo 描述一条已注册路由（名称、类型、优先级、是否 Once）。
type RouteInfo struct {
	Name     string
	Kind     string
	Priority int
	Once     bool
}

// RoutesFunc 返回路由快照，避免本包依赖 internal/router。
type RoutesFunc func(ctx context.Context) ([]RouteInfo, error)

// Cost 是一次成本查询的结果。
type Cost struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	AmountUSD        float64
}

// CostFunc 按 scope（"today" | "session"）查询成本（F-66）。
type CostFunc func(ctx context.Context, scope string) (Cost, error)

// ReloadFunc 手动触发一次热加载（F-24）；失败必须返回 error。
type ReloadFunc func(ctx context.Context) error

// HashSegment 是一段提示词的名字与哈希（F-65）。
type HashSegment struct {
	Name string
	Hash string
}

// PromptHashFunc 返回提示词各段哈希（F-65）。
type PromptHashFunc func(ctx context.Context) ([]HashSegment, error)

// statusHandler 把 StatusFunc 适配成命令处理器。
func statusHandler(fn StatusFunc) Handler {
	return func(ctx context.Context, _ Invocation) (string, error) {
		st, err := fn(ctx)
		if err != nil {
			return "", fmt.Errorf("admin: status: %w", err)
		}
		return formatStatus(st), nil
	}
}

// routesHandler 把 RoutesFunc 适配成命令处理器。
func routesHandler(fn RoutesFunc) Handler {
	return func(ctx context.Context, _ Invocation) (string, error) {
		routes, err := fn(ctx)
		if err != nil {
			return "", fmt.Errorf("admin: routes: %w", err)
		}
		return formatRoutes(routes), nil
	}
}

// costHandler 校验 scope 后查询成本。
func costHandler(fn CostFunc) Handler {
	return func(ctx context.Context, inv Invocation) (string, error) {
		if len(inv.Args) != 1 {
			return "", fmt.Errorf("admin: /cost 需要 today|session: %w", ErrUsage)
		}
		scope := strings.ToLower(inv.Args[0])
		if scope != "today" && scope != "session" {
			return "", fmt.Errorf("admin: /cost 未知范围 %q: %w", scope, ErrUsage)
		}
		c, err := fn(ctx, scope)
		if err != nil {
			return "", fmt.Errorf("admin: cost %s: %w", scope, err)
		}
		return formatCost(scope, c), nil
	}
}

// configHandler 处理 /config reload。
func configHandler(fn ReloadFunc) Handler {
	return func(ctx context.Context, inv Invocation) (string, error) {
		if len(inv.Args) != 1 || strings.ToLower(inv.Args[0]) != "reload" {
			return "", fmt.Errorf("admin: /config 用法为 /config reload: %w", ErrUsage)
		}
		if err := fn(ctx); err != nil {
			return "", fmt.Errorf("admin: config reload: %w", err)
		}
		return "配置已重载。", nil
	}
}

// promptHashHandler 把 PromptHashFunc 适配成命令处理器。
func promptHashHandler(fn PromptHashFunc) Handler {
	return func(ctx context.Context, _ Invocation) (string, error) {
		segs, err := fn(ctx)
		if err != nil {
			return "", fmt.Errorf("admin: prompt-hash: %w", err)
		}
		return formatHashes(segs), nil
	}
}

// formatStatus 渲染运行快照。
func formatStatus(s Status) string {
	return fmt.Sprintf("运行时长：%s\n事件数：%d\n活跃会话：%d\n队列深度：%d\n错误率：%.2f%%",
		s.Uptime.Round(time.Second), s.Events, s.ActiveSessions, s.QueueDepth, s.ErrorRate*100)
}

// formatRoutes 渲染路由快照；无路由时给出明确文案。
func formatRoutes(routes []RouteInfo) string {
	if len(routes) == 0 {
		return "没有已注册的路由。"
	}
	var b strings.Builder
	b.WriteString("已注册路由：\n")
	for _, r := range routes {
		once := "否"
		if r.Once {
			once = "是"
		}
		fmt.Fprintf(&b, "- %s | 类型=%s | 优先级=%d | Once=%s\n", r.Name, r.Kind, r.Priority, once)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatCost 渲染成本统计。
func formatCost(scope string, c Cost) string {
	return fmt.Sprintf("成本（%s）：prompt=%d completion=%d total=%d，约 %.6f USD",
		scope, c.PromptTokens, c.CompletionTokens, c.TotalTokens, c.AmountUSD)
}

// formatHashes 渲染提示词分段哈希；无分段时给出明确文案。
func formatHashes(segs []HashSegment) string {
	if len(segs) == 0 {
		return "没有可用的提示词分段。"
	}
	var b strings.Builder
	b.WriteString("提示词分段哈希：\n")
	for _, s := range segs {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Hash)
	}
	return strings.TrimRight(b.String(), "\n")
}
