package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrInvalidPolicy 表示沙箱策略在启动期校验失败。
var ErrInvalidPolicy = errors.New("invalid sandbox policy")

// 沙箱拒绝的稳定原因码。调用方/模型据此区分拒绝类型。
const (
	DenyForbiddenTool = "forbidden_tool"
	DenyReadOnly      = "read_only_required"
	DenyNetwork       = "network_disabled"
	DenyOperation     = "forbidden_operation"
	DenyReadPath      = "path_not_readable"
	DenyWritePath     = "path_not_writable"
	DenyEnv           = "env_not_allowed"
)

// DefaultMaxOutputBytes 是工具输出的默认上限（64 KiB）。
const DefaultMaxOutputBytes = 64 * 1024

// sandboxTruncatedMarker 是输出被截断时追加的标记。
const sandboxTruncatedMarker = "…[输出超限截断]"

// Denial 是一次被沙箱拒绝的结构化原因。
//
// 结构化而不是一句人话：拒绝要被包装成 observation 回灌模型，模型需要看到
// 稳定的 code 与字段，才能知道"违反了哪条边界"。
type Denial struct {
	Tool   string `json:"tool"`
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
}

// Error 实现 error；文本逐字节确定。
func (d Denial) Error() string {
	if d.Field != "" {
		return fmt.Sprintf("沙箱拒绝工具 %q（%s）：%s，字段=%s", d.Tool, d.Code, d.Detail, d.Field)
	}
	return fmt.Sprintf("沙箱拒绝工具 %q（%s）：%s", d.Tool, d.Code, d.Detail)
}

// JSON 返回结构化拒绝原因的 JSON 文本，字段顺序固定。
func (d Denial) JSON() string {
	b, err := json.Marshal(d)
	if err != nil {
		return `{"tool":"` + d.Tool + `","code":"` + d.Code + `"}`
	}
	return string(b)
}

// Result 把拒绝转成回灌模型的结果（不返回 error，让 ReAct 循环继续）。
func (d Denial) Result() Result {
	return Result{
		Error: d.Error(),
		Metadata: map[string]string{
			"sandbox_denied": "true",
			"code":           d.Code,
			"field":          d.Field,
			"denial":         d.JSON(),
		},
	}
}

// SandboxRequest 是工具为一次调用声明的资源需求。
//
// 由工具自己声明，而不是沙箱去解析参数 JSON：只有工具知道参数里哪个字段是路径、
// 哪个是命令。声明是可选的；未声明的工具按"零需求"处理。
type SandboxRequest struct {
	ReadPaths  []string
	WritePaths []string
	Env        []string
	Ops        []string
	Network    bool
}

// SandboxDeclarer 由需要资源声明的工具实现。
type SandboxDeclarer interface {
	SandboxRequest(args json.RawMessage) SandboxRequest
}

// Policy 是工具执行沙箱的权限边界。
//
// 默认全部关闭：路径、环境变量都要显式出现在白名单里，网络只有列进 NetworkTools
// 的工具才允许。零值 Policy 是"只允许无资源需求的工具"。
type Policy struct {
	// MaxOutputBytes 是单次工具输出/错误文本的字节上限；<=0 时用 DefaultMaxOutputBytes。
	MaxOutputBytes int
	// ReadRoots / WriteRoots 是只读、可写路径白名单（绝对路径）。
	// 可写目录同时视为可读。
	ReadRoots  []string
	WriteRoots []string
	// EnvAllowlist 是允许工具读取/设置的环境变量名。
	EnvAllowlist []string
	// ForbiddenOps 是禁止的操作名；声明里出现即拒绝。
	ForbiddenOps []string
	// ForbiddenTools 是整类禁止的工具名。
	ForbiddenTools []string
	// NetworkTools 是显式允许联网的工具名；不在其中且未 AllowNetwork 时禁止网络。
	NetworkTools []string
	// AllowNetwork 为 true 时对所有工具放行网络（默认 false）。
	AllowNetwork bool
	// RequireReadOnly 为 true 时只放行声明为只读的工具。
	RequireReadOnly bool
}

// Validate 在启动期校验策略；策略非法时整个沙箱不得装配。
func (p *Policy) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: 策略为 nil", ErrInvalidPolicy)
	}
	if p.MaxOutputBytes < 0 {
		return fmt.Errorf("%w: MaxOutputBytes 不能为负: %d", ErrInvalidPolicy, p.MaxOutputBytes)
	}
	for _, root := range p.ReadRoots {
		if err := validateRoot("只读", root); err != nil {
			return err
		}
	}
	for _, root := range p.WriteRoots {
		if err := validateRoot("可写", root); err != nil {
			return err
		}
	}
	for _, name := range p.EnvAllowlist {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: 环境变量白名单含空名", ErrInvalidPolicy)
		}
		if strings.ContainsRune(name, '=') {
			return fmt.Errorf("%w: 环境变量白名单 %q 含 '='", ErrInvalidPolicy, name)
		}
	}
	for _, op := range p.ForbiddenOps {
		if strings.TrimSpace(op) == "" {
			return fmt.Errorf("%w: ForbiddenOps 含空项", ErrInvalidPolicy)
		}
	}
	for _, name := range p.ForbiddenTools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: ForbiddenTools 含空项", ErrInvalidPolicy)
		}
	}
	for _, name := range p.NetworkTools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: NetworkTools 含空项", ErrInvalidPolicy)
		}
	}
	return nil
}

func validateRoot(kind, root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("%w: %s路径为空", ErrInvalidPolicy, kind)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%w: %s路径必须是绝对路径: %q", ErrInvalidPolicy, kind, root)
	}
	return nil
}

// maxOutput 返回生效的输出上限。
func (p *Policy) maxOutput() int {
	if p.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return p.MaxOutputBytes
}

// Check 校验一次声明的资源需求；允许时返回 nil，否则返回结构化拒绝原因。
func (p *Policy) Check(toolName string, req SandboxRequest) *Denial {
	if p == nil {
		return &Denial{Tool: toolName, Code: DenyForbiddenTool, Detail: "没有沙箱策略"}
	}
	for _, name := range p.ForbiddenTools {
		if name == toolName {
			return &Denial{Tool: toolName, Code: DenyForbiddenTool, Detail: "该工具被策略禁止"}
		}
	}
	if req.Network && !p.networkAllowed(toolName) {
		return &Denial{Tool: toolName, Code: DenyNetwork, Detail: "策略默认禁止网络，且该工具未显式开启"}
	}
	for _, op := range req.Ops {
		for _, bad := range p.ForbiddenOps {
			if op == bad {
				return &Denial{Tool: toolName, Code: DenyOperation, Field: op, Detail: "危险操作被策略禁止"}
			}
		}
	}
	for _, path := range req.ReadPaths {
		if !withinAny(path, p.ReadRoots) && !withinAny(path, p.WriteRoots) {
			return &Denial{Tool: toolName, Code: DenyReadPath, Field: path, Detail: "路径不在只读白名单内"}
		}
	}
	for _, path := range req.WritePaths {
		if !withinAny(path, p.WriteRoots) {
			return &Denial{Tool: toolName, Code: DenyWritePath, Field: path, Detail: "路径不在可写白名单内"}
		}
	}
	for _, name := range req.Env {
		if !containsString(p.EnvAllowlist, name) {
			return &Denial{Tool: toolName, Code: DenyEnv, Field: name, Detail: "环境变量不在白名单内"}
		}
	}
	return nil
}

func (p *Policy) networkAllowed(toolName string) bool {
	if p.AllowNetwork {
		return true
	}
	return containsString(p.NetworkTools, toolName)
}

// withinAny 判断 path 是否落在任一白名单根目录下（含根本身）。
func withinAny(path string, roots []string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		cr := filepath.Clean(root)
		if clean == cr {
			return true
		}
		if strings.HasPrefix(clean, cr+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// SandboxOption 配置 SandboxTool。
type SandboxOption func(*SandboxTool)

// WithDenyHook 注入拒绝回调（审计用）。
func WithDenyHook(fn func(Denial)) SandboxOption {
	return func(s *SandboxTool) { s.onDeny = fn }
}

// SandboxTool 是执行沙箱装饰器：在不改变 Tool 契约的前提下，对每次调用做权限判定、
// 输出限长，并在拒绝时返回结构化的失败 Result（内层 Execute 不会被调用）。
type SandboxTool struct {
	inner  Tool
	policy Policy
	onDeny func(Denial)
}

// NewSandboxTool 包装一个工具；策略为空或非法时返回错误（启动期校验）。
func NewSandboxTool(inner Tool, p *Policy, opts ...SandboxOption) (*SandboxTool, error) {
	if inner == nil {
		return nil, fmt.Errorf("%w: 被包装的工具为 nil", ErrNilTool)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	s := &SandboxTool{inner: inner, policy: *p}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Name 透传内层工具名，保证注册顺序与前缀缓存不变。
func (s *SandboxTool) Name() string { return s.inner.Name() }

// Description 透传内层描述。
func (s *SandboxTool) Description() string { return s.inner.Description() }

// Parameters 透传内层 schema。
func (s *SandboxTool) Parameters() Schema { return s.inner.Parameters() }

// Unwrap 返回内层工具（供测试与装配检查）。
func (s *SandboxTool) Unwrap() Tool { return s.inner }

// ReadOnly 委托内层声明，避免包装改变审批语义。
func (s *SandboxTool) ReadOnly() bool { return IsReadOnly(s.inner) }

// ConcurrencySafe 委托内层声明。
func (s *SandboxTool) ConcurrencySafe() bool { return IsConcurrencySafe(s.inner) }

// Dangerous 委托内层声明。
func (s *SandboxTool) Dangerous() bool { return IsDangerous(s.inner) }

// Execute 先做沙箱判定，再委托内层；输出超限时截断且不阻塞。
func (s *SandboxTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	name := s.inner.Name()
	if s.policy.RequireReadOnly && !IsReadOnly(s.inner) {
		return s.deny(Denial{Tool: name, Code: DenyReadOnly, Detail: "策略只允许只读工具"})
	}
	var req SandboxRequest
	if d, ok := s.inner.(SandboxDeclarer); ok {
		req = d.SandboxRequest(args)
	}
	if d := s.policy.Check(name, req); d != nil {
		return s.deny(*d)
	}
	res, err := s.inner.Execute(ctx, args)
	if err != nil {
		return res, err
	}
	return s.clamp(res), nil
}

func (s *SandboxTool) deny(d Denial) (Result, error) {
	if s.onDeny != nil {
		s.onDeny(d)
	}
	return d.Result(), nil
}

// clamp 按字节上限截断输出与错误文本，并在 Metadata 标注。
func (s *SandboxTool) clamp(res Result) Result {
	max := s.policy.maxOutput()
	if len(res.Output) > max {
		res.Output = truncateUTF8(res.Output, max)
		res.Metadata = withMeta(res.Metadata, "sandbox_truncated", "output")
	}
	if len(res.Error) > max {
		res.Error = truncateUTF8(res.Error, max)
		res.Metadata = withMeta(res.Metadata, "sandbox_truncated", "error")
	}
	return res
}

func withMeta(m map[string]string, key, value string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[key] = value
	return m
}

// truncateUTF8 把 s 截到不超过 max 字节（含标记），并保证不切断 UTF-8 字符。
func truncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	keep := max - len(sandboxTruncatedMarker)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + sandboxTruncatedMarker
}
