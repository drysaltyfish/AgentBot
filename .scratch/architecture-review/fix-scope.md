# Fix C5 · 记忆作用域键独立成模块（task-6）

## 结论

**已实现。** ctx 里"本轮属于哪个会话"的键现在只有一份定义，位于新模块
`internal/scope`；`internal/memory` 不再依赖 `internal/tool`；
`internal/agent` 的 `WithMemoryScope` / `MemoryScopeFrom` 旧名保留，纯委托。

## 变更清单

| 文件 | 动作 |
| --- | --- |
| `internal/scope/scope.go` | 新建：`type scopeKey struct{}`、`WithScope(ctx, scope)`、`ScopeFrom(ctx)`。语义与旧 `internal/tool/scope.go` 完全一致（缺失/nil ctx 返回空串）。 |
| `internal/scope/scope_test.go` | 新建：往返、缺失为空、嵌套覆盖三个用例锁住语义。 |
| `internal/tool/scope.go` | **删除**。键的定义迁走后本文件只剩转发；`internal/tool` 内没有任何其它代码使用这两个符号，仓库其它调用方也全部在本次写范围内，故不保留 deprecated 垫片。 |
| `internal/memory/store.go` | 导入 `internal/scope` 替换 `internal/tool`；6 处 `tool.ScopeFrom` → `scope.ScopeFrom`。`Save` 内局部变量 `scope` 改名 `scopeKey` 以免遮蔽包名。 |
| `internal/memory/memory_test.go` | 改用 `scope.WithScope`；helper 参数改名 `key` 以免遮蔽包名。 |
| `internal/tool/builtin/history.go` | `recall_history` 改用 `scope.ScopeFrom`，并更新注释。 |
| `internal/tool/builtin/userinfo.go` | `get_user_info` 改用 `scope.ScopeFrom`。 |
| `internal/tool/builtin/builtin_test.go` | 11 处 `tool.WithScope` → `scope.WithScope`（`tool.Registry` 等用法不变）。 |
| `internal/agent/memory.go` | 导入 `internal/scope` 替换 `internal/tool`；`WithMemoryScope`/`MemoryScopeFrom` **保留同名导出符号**，函数体改为 `scope.WithScope` / `scope.ScopeFrom`；参数由 `scope` 改名 `key` 以避免遮蔽。 |

## 符号归属（报告要求）

- 移动：键类型 `turnScopeKey{}` → `internal/scope.scopeKey{}`；
  `tool.WithScope` → `scope.WithScope`；`tool.ScopeFrom` → `scope.ScopeFrom`。
- 删除：`internal/tool/scope.go` 与其中的 `tool.WithScope` / `tool.ScopeFrom` / `turnScopeKey`。
- 保留的别名：`agent.WithMemoryScope`、`agent.MemoryScopeFrom`（唯一实现是 `internal/scope`，agent 不再导入 `internal/tool`）。
  保留原因：`cmd/server/main.go:1297` 与 `internal/agent/react.go:89` 都调用 `WithMemoryScope`，而 react.go/main.go 不在本任务写范围（Lead 所有）。

## 未改动及其理由

- `internal/agent/react.go`、`cmd/server/main.go`：Lead 所有，按协调规则不碰；靠保留别名保持编译。
- `docs/adr/0002-memory-injection-position.md`：ADR 只约定"按作用域隔离 + 经 ctx 传递 + `WithMemoryScope`"，模块搬家不改行为，无需改 ADR。
- `internal/memory` 的校验/存储逻辑与 `builtin` 的其它 `tool` 用法：不在本候选范围，未动。

## 验证命令与结果（均在 C:\AgentBot 下）

```
gofmt -l internal/scope internal/memory internal/agent/memory.go internal/tool
  -> 无输出

go vet ./internal/scope/... ./internal/memory/... ./internal/tool/...
  -> exit 0，无输出

go test ./internal/scope/... ./internal/memory/... ./internal/tool/...
  -> ok internal/scope 0.593s
  -> ok internal/memory 0.091s
  -> ok internal/tool 6.671s
  -> ok internal/tool/builtin 7.256s

go build ./internal/agent/
  -> exit 0（证明 agent/memory.go 的别名与当前 react.go 一起编译通过）

grep -rn "internal/tool" internal/memory/
  -> 0 命中（internal/memory 不再依赖 internal/tool）

grep -rn "tool.(ScopeFrom|WithScope)" .
  -> 0 命中
```

## 关于 `go test ./internal/agent/...`

**未通过，但与本次改动无关。** 当前失败是 Lead 正在进行的重构：测试文件
`agent_test.go` / `memory_scope_test.go` / `prompt_test.go` / `virtual_test.go`
引用已删除的 `ReactAgent.SystemPrompt` 字段，且采集时 `agent_test.go:68`
正处于半编辑状态（syntax error）。所有报错均为 `SystemPrompt`/语法，
没有一处指向 `scope` 或记忆作用域符号；非测试包 `go build ./internal/agent/` 通过。
作用域隔离行为由 `internal/memory`（scope.WithScope 写入、ScopeFrom 读取）与
`internal/tool/builtin`（RecallHistory 按 scope 取值）的通过用例覆盖；
跨包同键由"只有一份键定义"从结构上保证。请 Lead 在 agent 测试恢复后补跑
`go test ./internal/agent/...`。

## ADR-0002 影响

无行为变化：作用域仍经 ctx 传递、会话键即作用域、缺失时 memory.Save 拒绝写入。
仅键的宿主包改变，隔离语义不变。
