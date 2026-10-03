# 35 · 虚拟动作闭环

- **Feature**: F-40（P1）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 34
- **写域（建议）**: internal/agent/

## 目标

有些动作不需要宿主执行（结束本轮、保存记忆），必须由库内部处理，否则模型会等一个永远不会来的响应。

## 交付物

- 保留动作名：`end_action` / `save_memory` / `noop`
- 虚拟动作由库内部执行，并**伪造一条 ok 结果回灌历史**，保持"调用→观察"循环完整
- 非虚拟动作返回给宿主执行，宿主回灌真实结果
- 控制流用哨兵错误：`var ErrEndOfTurn = errors.New(...)`

## 验收

- 模型输出 `end_action` 后循环终止且**不发送任何消息**
- `save_memory` 写入成功后在下次会话的提示词中出现该记忆
- 虚拟动作也纳入审计日志（F-60）

## 边界与易错点

- `save_memory` 参数必须是单行文本，含换行则拒绝并回灌错误结果；空记忆拒绝写入
- **不使用 `io.EOF`** 表达控制流（语义模糊，易与网络错误混淆）

## 备注（已定决策）

- 记忆注入位置见 `docs/adr/0002-memory-injection-position.md`：system 之后、历史之前，长度上限 2 KiB，内容确定性排序


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/agent/virtual.go`、`internal/agent/memory.go`、`internal/agent/virtual_test.go`（9 个用例），
以及 ReactAgent 的两处接线（记忆注入、end_action 终止）。

**虚拟动作就是普通工具**（与 ADR-0001 一致）：`endTurnTool` / `saveMemoryTool` / `noopTool`
都注册进 F-41 的注册表，用 `VirtualAction` 标记"由库内部执行"。这样"伪造 ok 结果回灌"
就是一条普通的 tool 消息，不需要第二套协议。

**控制流**：`end_action` 通过 `Result.Metadata["control"]="end_of_turn"` 把意图传回循环，
`Run` 返回哨兵错误 `ErrEndOfTurn` 并置 `FinishReason=end_of_turn`。
按 F-40 明确要求**不使用 io.EOF**。约定写在错误注释里：`ErrEndOfTurn` 不是失败，
调用方应据此**不发送任何消息**。

**记忆注入位置**严格遵循 ADR-0002（system 之后、历史之前），并有专门测试断言
五条消息的顺序：system → 记忆 → 历史1 → 历史2 → 当前输入。

**校验失败一律回灌**：空记忆、多行记忆、超长记忆、未配置 Memory 四种情况都返回
`tool.Failure`（而不是 error），循环继续并把原因交给模型。
其中"未配置记忆"刻意返回失败而不是静默成功——让模型以为写成功了是最糟的。

**验收执行**：

```
go test -count=1 ./internal/agent/   → ok（21 个用例）
go test -count=1 ./...               → 18 个包全绿
golangci-lint run                    → 0 issues
```

覆盖：end_action 终止且不再调用 LLM、save_memory 在**下一次 Run** 的提示词中出现、
注入位置、两种非法记忆被拒且回灌、noop、注册表里三个动作齐全、
记忆有界（超限丢最旧）与去重、渲染确定性。

**一处超出规格但必要的东西**：`Memory` 接口 + 进程内 `MemoryStore`。
F-48 的完整实现（持久化/向量召回）在 M3，但验收要求"写入后在下次会话出现"，
没有最小存储就无从验证。接口刻意保持最小（Save/Recall），不提前钉死 M3 的设计。
