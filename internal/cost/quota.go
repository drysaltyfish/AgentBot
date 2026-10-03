package cost

import (
	"fmt"
	"time"
)

// Scope 是配额的统计维度。
type Scope string

const (
	// ScopeGlobal 是全局维度（所有会话、所有用户）。
	ScopeGlobal Scope = "global"
	// ScopeSession 是按会话维度。
	ScopeSession Scope = "session"
	// ScopeUser 是按用户维度。
	ScopeUser Scope = "user"
)

// Period 是配额的重置周期。
type Period string

const (
	// PeriodDay 按自然日重置。
	PeriodDay Period = "day"
	// PeriodMonth 按自然月重置。
	PeriodMonth Period = "month"
	// PeriodTotal 永不重置（会话生命周期内累计）。
	PeriodTotal Period = "total"
)

// Action 是达到硬限后的动作。
type Action string

const (
	// ActionDeny 拒绝请求（默认动作），Authorize 返回 *QuotaError。
	ActionDeny Action = "deny"
	// ActionDowngrade 放行但要求切换到 DowngradeModel。
	ActionDowngrade Action = "downgrade"
	// ActionWarn 仅告警，不阻断。
	ActionWarn Action = "warn"
)

// defaultSoftRatio 是未显式配置 SoftLimit 时的软限比例（FEATURES.md 的 80%）。
const defaultSoftRatio = 0.8

// Quota 是一条配额定义：{scope, limit, period, action}，并支持两档阈值。
//
// 软限（SoftLimit，默认 80% × Limit）只告警一次；硬限（Limit）按 Action
// 拒绝、降级或仅告警，同样每个周期窗口只告警一次。
type Quota struct {
	// Scope 是统计维度，必填。
	Scope Scope
	// Period 是重置周期，必填。
	Period Period
	// Limit 是硬限（美元），必须大于 0。
	Limit float64
	// SoftLimit 是软限（美元）；<=0 时取 80% × Limit。
	SoftLimit float64
	// Action 是达到硬限后的动作，必填；默认 deny。
	Action Action
	// DowngradeModel 在 Action 为 downgrade 时必填，是建议切换到的模型。
	DowngradeModel string
}

// validate 校验配额定义，构造期失败优于运行期静默失效。
func (q Quota) validate() error {
	switch q.Scope {
	case ScopeGlobal, ScopeSession, ScopeUser:
	default:
		return fmt.Errorf("invalid scope %q", q.Scope)
	}
	switch q.Period {
	case PeriodDay, PeriodMonth, PeriodTotal:
	default:
		return fmt.Errorf("invalid period %q", q.Period)
	}
	switch q.Action {
	case ActionDeny, ActionDowngrade, ActionWarn:
	default:
		// 空 action 由 New 归一化为 ActionDeny，因此走到这里的是真正的非法值。
		return fmt.Errorf("invalid action %q", q.Action)
	}
	if q.Limit <= 0 {
		return fmt.Errorf("limit must be positive, got %v", q.Limit)
	}
	if q.SoftLimit < 0 {
		return fmt.Errorf("soft_limit must not be negative, got %v", q.SoftLimit)
	}
	if q.Action == ActionDowngrade && q.DowngradeModel == "" {
		return fmt.Errorf("action downgrade requires downgrade_model")
	}
	return nil
}

// softLimit 返回生效的软限金额。
func (q Quota) softLimit() float64 {
	if q.SoftLimit > 0 {
		return q.SoftLimit
	}
	return defaultSoftRatio * q.Limit
}

// periodStart 返回当前时刻所在周期窗口的键；total 无窗口。
func (q Quota) periodStart(now time.Time) string {
	switch q.Period {
	case PeriodDay:
		return now.Format("2006-01-02")
	case PeriodMonth:
		return now.Format("2006-01")
	default:
		return ""
	}
}

// Decision 是一次配额判定结果。
type Decision struct {
	// Allowed 为 false 表示硬限动作是 deny。
	Allowed bool
	// Action 是最终生效的动作（最严格者优先：deny > downgrade > warn）。
	Action Action
	// Reason 是可读的判定说明。
	Reason string
	// Scope、Period、Limit、Used 描述触发判定的配额。
	Scope  Scope
	Period Period
	Limit  float64
	Used   float64
	// SoftLimit 表示已达到软限（可能有多个配额）。
	SoftLimit bool
	// DowngradeModel 在 Action 为 downgrade 时给出建议模型。
	DowngradeModel string
}

// QuotaError 是硬限拒绝的结构化错误，调用方可用 errors.As 取出。
type QuotaError struct {
	Scope  Scope
	Period Period
	Limit  float64
	Used   float64
}

// Error 实现 error。
func (e *QuotaError) Error() string {
	return fmt.Sprintf("cost: quota exceeded: scope=%s period=%s limit=%.4f used=%.4f", e.Scope, e.Period, e.Limit, e.Used)
}
