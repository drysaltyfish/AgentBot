# 38 · 工具权限与人工审批

- **Feature**: F-45（P1）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 34
- **写域（建议）**: internal/tool/, internal/policy/

## 目标

踢人、禁言、发钱这类动作不能由模型自由调用，需要角色校验或人工确认。

## 交付物

- 权限表：按工具名 × 角色（群主/管理/成员/私聊）决定 allowed / denied / needs_approval
- 审批通道：请求发出后等待宿主确认，超时视为拒绝并回灌结果
- 审批等待占用**独立预算**，不计入 F-35 的单步工具超时

## 验收

- 被拒工具不执行且回灌明确的拒绝原因（可被模型理解）
- 审批超时回灌超时结果，循环继续而非中断
- 审批等待确实不占用单步超时预算（用可控时钟断言）

## 边界与易错点

- 默认拒绝（fail-closed）：权限表未列出的工具视为 needs_approval，而不是放行
- 审批结果必须进审计日志（F-60）

## 备注（已定决策）

- 与 F-53「权限即提示词」配合：被拒工具应同时从提示词里体现，减少无效调用


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/agent/approval.go`、`internal/agent/approval_test.go`（8 个用例），
以及 ReactAgent 的审批闸门。

- `Verdict`（allow/deny/approve）、`Gate` 判定接口、`Approver` 人工通道接口、`ApprovalRecord` 审计记录
- `TableGate`：「工具 × 角色 → 判定」的表，**fail-closed**——表里没有的工具、
  或该工具没有为该角色配置，一律判为**需要审批**而不是放行
- ReactAgent 新增 `Gate` / `Approver` / `ApprovalTimeout` / `OnApproval`；
  `Input.Role` 承载发起者角色

**最容易做错的一条已专门守住**：审批等待必须在**单步超时之外**。
若把审批塞进 `StepTimeout` 的 ctx 里，"等人确认"会把工具的执行预算耗光，
表现为审批通过后工具立刻超时。
`Test_F45_ApprovalWaitDoesNotEatStepTimeout` 用 40ms 单步超时 + 120ms 审批等待 +
5ms 工具执行来验证：工具必须正常执行且**不被误判为超时**。

**其他边界**：
- 需要审批但未配置 `Approver` → 拒绝执行并回灌原因（不静默放行）
- 审批超时或返回错误 → 按拒绝处理（不确定的副作用不该执行），循环继续
- `VerdictDeny` → 不执行、不调用审批通道，回灌策略拒绝
- 所有判定（含超时）都写 `ApprovalRecord` 审计（F-60 的接入点）

**验收执行**：`go test -count=1 ./internal/agent/` → ok（29 个用例）；全 19 包绿；
golangci-lint 0 issues。

**未接入组合根**：`Gate`/`Approver` 目前只在 `ReactAgent` 上可用，`cmd/server` 尚未配置它们
（M2 的主流程仍是直连 LLM）。机器人要真正走 ReAct + 审批，需要在组合根把
`ReactAgent` 接进回复链路——那是把 M2 能力接入生产路径的下一步。
