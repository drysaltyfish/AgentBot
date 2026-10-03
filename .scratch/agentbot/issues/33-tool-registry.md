# 33 · 工具注册表与自描述接口

- **Feature**: F-41, F-42（均 P0）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 无
- **写域（建议）**: internal/tool/

## 目标

Agent 能用什么能力由注册表决定。它必须并发安全、可自省，并能一键导出成模型的 function schema。

## 交付物

- `type Registry struct { mu sync.RWMutex; tools map[string]Tool; order []string }`
- `Register(t Tool) error`（重名报错）/ `MustRegister`（重名 panic，仅 init 期）/ `Get` / `List() []Tool`（按注册顺序）/ `Names`
- `Definitions() []llm.ToolSpec`：导出给模型的 schema
- `Subset(names ...string) *Registry`（供不同 Worker 用不同工具集，F-37）/ `Remove` / `Clear`
- `type Tool interface { Name() string; Description() string; Parameters() Schema; Execute(ctx, json.RawMessage) (Result, error) }`
- `Schema/Property`、`Result`（`Success/Failure/String`）、属性声明 `ReadOnly/ConcurrencySafe/Dangerous`

## 验收

- 注册 3 个工具，两次 `Definitions()` 结果**逐字节相同**（顺序稳定）
- `Subset` 不影响父注册表；并发 `Register` 与 `Definitions` 在 `-race` 下无告警
- 非法工具名（不匹配 `^[a-zA-Z0-9_-]{1,64}$`）在 `Register` 时返回 error
- `Result.String()` 在 Error 非空时返回错误文本

## 边界与易错点

- `List` 顺序必须稳定（用 `order` 切片维护），否则每次生成的提示词不同 → **直接破坏前缀缓存**
- `Description()` 上限默认 1024 字符，超出截断并告警
- `Execute` 返回的 error 会被包装成 observation 回灌模型，**不得 panic**

## 备注（已定决策）

- 顺序稳定不是洁癖：它同时影响 F-65 的前缀缓存与提示词可复现性
- 见 `docs/adr/0001-tool-call-protocol.md`：虚拟动作也是注册表里的普通工具


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/tool/`（tool.go + registry.go + registry_test.go）

- `Registry`：`Register` / `MustRegister` / `Get` / `List` / `Names` / `Len` / `Definitions` /
  `Subset` / `Remove` / `Clear`，内部用 `order` 切片维持注册顺序
- `Tool` 接口 + `Schema`/`Property`（含 `JSONSchema()`）+ `Result`（`Success`/`Failure`/`Failed`/`String`）
- `Definitions()` 直接产出 `[]llm.ToolSpec`，与模型层对接

**顺序稳定性如何保证**：`List`/`Definitions` 严格按注册顺序；`Schema.JSONSchema` 依赖
`encoding/json` 对 map 键排序的既定行为。测试对两者都做了多次调用 + 逐字节比较。

**验收执行**：

```
go test -count=1 ./internal/tool/     → ok（11 个用例）
go test -count=1 ./...                → 18 个包全绿
golangci-lint run                     → 0 issues
gofmt -l ./cmd ./internal             → 空
go build ./... / go vet ./...         → exit 0
```

覆盖的验收点：`Definitions()` 逐字节稳定（连续 6 次）、`Subset` 与父表双向隔离、
非法名（空/空格/中文/超 64/含斜杠）与重名被拒、`Remove` 后顺序与稳定性、
16 并发 `Register` + 16 并发读 `Definitions`/`List`/`Names`、描述超限截断并告警、
`Result` 的失败语义、`Schema` 键序稳定。

**两处刻意偏离规格，理由如下**：

1. **可选属性用可选接口而非塞进 `Tool`**。规格在 `Tool` 接口旁列了
   `ReadOnly()`/`ConcurrencySafe()`/`Dangerous()`。若放进主接口，每个工具都得写三个
   返回 false 的方法，纯噪音。改为 `ReadOnlyTool`/`ConcurrencySafeTool`/`DangerousTool`
   可选接口 + `IsReadOnly`/`IsConcurrencySafe`/`IsDangerous` 取值函数。
   **默认值取保守侧**：未声明时 `IsReadOnly=false`、`IsConcurrencySafe=false`（串行）、
   `IsDangerous=true`（走审批）——不确定时按更严格的一侧。
   测试里专门放了 `bareTool` 类型来覆盖"未声明"这条路径：`fakeTool` 实现了全部三个
   可选接口，用它测未声明会永远走已声明分支（这一点是本轮测试自己先写错、被跑出来的）。

2. **`MustRegister` 会 panic，带定向豁免**。F-73 把 panic 列为禁令，但同一份规格明确写了
   "init 期断言除外"；`Must*` 是 Go 的既定惯例（`regexp.MustCompile`、`template.Must`）。
   因此加 `//nolint:forbidigo` 并写明理由，而不是绕过 lint。
