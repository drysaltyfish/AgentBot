# 41 · Agent 接入组合根（M2 收口）

- **Feature**: F-35 / F-41 / F-44 / F-45 的接线（M2 完成判据）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 34, 37, 38, 39
- **写域（建议）**: cmd/server/, internal/config/, internal/conversation/

## 目标

把 M2 实现的工具系统从"可用的库"变成"机器人真的在用"：
回复链路走 ReAct，工具注册进去，记忆按 ADR-0002 注入。

## 交付物

- `config.Agent`：enabled / max_iterations / step_timeout / protocol / tools /
  virtual_actions / memory / memory_max / approval_enabled / allow / approval_timeout
- `cmd/server.buildAgent`：按配置装配 ReactAgent；未启用时返回 DirectAgent，
  调用方对两条路径完全同形
- `handleReply` 改为调用 `agent.Agent`；`ErrEndOfTurn` 被识别为"不发消息"而非失败
- `conversation.ToMessages` 导出：历史→消息的映射只有一份，避免两条路径漂移
- `agentRole`：把平台上报的成员角色（owner/admin/member）映射成 F-45 的权限角色

## 验收

- 启动日志打印实际注册的工具清单与顺序
- 未启用 ReAct 时行为与之前完全一致（直连 LLM）
- `end_action` 不发送任何消息，但该轮仍进历史

## Comments

### 2026-10-03 · 完成记录

**启动实测**（`config.yaml`，agent.enabled=true）：

```
msg="react agent enabled" component=agent
  tools="[calculator current_time json_query http_fetch memory_save memory_recall end_action save_memory noop]"
  max_iterations=10 protocol=auto step_timeout=30s memory=true approval=false
```

6 个内置工具 + 3 个虚拟动作按固定顺序注册（顺序稳定直接关系前缀缓存）。

**几处刻意的设计选择**：

1. `buildAgent` 返回 `agent.Agent` 接口而不是具体类型：未启用 ReAct 时返回
   `DirectAgent`，因此 `handleReply` 里**没有 if/else 分支**，两条路径同形。
2. `conversation.ToMessages` 被导出并让 `Assembler.Build` 复用：历史→消息的映射
   必须只有一份。它同时影响 ReAct 与直连两条路径，也影响前缀缓存。
3. `approval_enabled` **默认关闭**：权限表是 fail-closed 的，没配审批通道就打开会让
   所有未放行工具都被拒绝。配置校验里加了保护——启用审批但 allow 为空时直接报错。
4. M2 尚未接入交互式审批通道，因此启用审批时未放行的调用会被**明确拒绝并回灌原因**，
   而不是静默放行（fail-closed 的正确表现），并在启动时打 WARN。

**验证**：19 包全绿；golangci-lint 0 issues；gofmt/build/vet 干净；
`--check-config` 通过；启动日志确认工具注册与顺序。

**仍未完成（M2 完成判据的另一半）**：F-75 的"多轮工具调用契约"尚未在真实平台上跑通。
机制层面已有一条线路级契约测试（ticket 34 用 mock server 断言第二轮请求体的
`tool_calls`/`tool_call_id`），但"真实 NapCat + DeepSeek 上多轮工具调用可用"
还需要一次联调验证——NapCat 目前处于停止状态，重启需要重新扫码登录。
