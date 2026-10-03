package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrApprovalDenied 表示审批被拒绝（含超时视为拒绝）。
	ErrApprovalDenied = errors.New("tool call denied")
	// ErrNoApprover 表示判定为需要审批但没有配置审批通道。
	ErrNoApprover = errors.New("tool call needs approval but no approver is configured")
)

// DefaultApprovalTimeout 是审批等待的默认独立预算。
const DefaultApprovalTimeout = 60 * time.Second

// Verdict 是权限判定的结果。
type Verdict int

const (
	// VerdictAllow 允许直接执行。
	VerdictAllow Verdict = iota
	// VerdictDeny 直接拒绝。
	VerdictDeny
	// VerdictApprove 需要人工审批。
	VerdictApprove
)

// String 实现 fmt.Stringer。
func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictDeny:
		return "deny"
	case VerdictApprove:
		return "approve"
	default:
		return "unknown"
	}
}

// Role 是发起调用的角色（F-45 的权限维度之一）。
type Role string

// 角色常量。
const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RolePrivate Role = "private"
)

// ApprovalRequest 是一次审批请求。
type ApprovalRequest struct {
	ToolName  string
	Arguments string
	Reason    string
	Role      Role
	GroupID   int64
	UserID    int64
}

// Decision 是审批结果。
type Decision struct {
	Allowed bool
	Reason  string
}

// Gate 判定一次工具调用是否需要审批。
type Gate interface {
	Check(req ApprovalRequest) Verdict
}

// Approver 是人工审批通道（宿主实现：IM 里发确认卡片、CLI 里问一句等）。
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRecord 是一条审批审计记录（F-60）。
type ApprovalRecord struct {
	At       time.Time
	Request  ApprovalRequest
	Verdict  Verdict
	Allowed  bool
	Reason   string
	WaitMS   int64
	TimedOut bool
}

// TableGate 是一张"工具 × 角色 -> 判定"的权限表。
//
// **fail-closed**：表里没有的工具、或该工具没有为该角色配置，
// 一律判定为**需要审批**，而不是放行。
type TableGate struct {
	mu    sync.RWMutex
	rules map[string]map[Role]Verdict
}

// NewTableGate 构造空表。
func NewTableGate() *TableGate {
	return &TableGate{rules: map[string]map[Role]Verdict{}}
}

// Set 写入一条规则。
func (g *TableGate) Set(tool string, role Role, v Verdict) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rules[tool] == nil {
		g.rules[tool] = map[Role]Verdict{}
	}
	g.rules[tool][role] = v
}

// Check 实现 Gate。
func (g *TableGate) Check(req ApprovalRequest) Verdict {
	g.mu.RLock()
	defer g.mu.RUnlock()
	byRole, ok := g.rules[req.ToolName]
	if !ok {
		// 表里没有的工具：不确定就按更严格的一侧处理。
		return VerdictApprove
	}
	v, ok := byRole[req.Role]
	if !ok {
		return VerdictApprove
	}
	return v
}

// ApproverFunc 让裸函数实现 Approver（测试与简单宿主用）。
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (Decision, error)

// Approve 实现 Approver。
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (Decision, error) {
	return f(ctx, req)
}
