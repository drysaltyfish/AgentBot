# 33 · 工具注册表与自描述接口

- **Feature**: F-41, F-42（均 P0）
- **里程碑**: M2
- **Status**: open
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
