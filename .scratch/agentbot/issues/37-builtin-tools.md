# 37 · 内置安全工具集

- **Feature**: F-44（P1）
- **里程碑**: M2
- **Status**: open
- **Blocked by**: 33
- **写域（建议）**: internal/tool/builtin/

## 目标

开箱可用的基础能力，且必须**零注入面**。

## 交付物

- `calculator`：`go/parser` 解析算式，白名单节点（BasicLit/BinaryExpr/UnaryExpr/ParenExpr）递归求值
- `current_time`：指定时区的 RFC3339 时间，校验时区合法性
- `http_fetch`：GET 返回文本（仅 http/https、拒私网、超时 10s、响应上限 1 MiB、非文本类型拒绝、可选域名白名单）
- `memory_save` / `memory_recall`：包装 F-48
- `json_query`：路径表达式取值（只读，无代码执行）

## 验收

- `calculator` 对 `__import__("os")`、`1+`、`((1+2)*3` 全部返回错误而不 panic
- `http_fetch` 访问 `http://127.0.0.1/` 与 `http://169.254.169.254/` 被拒绝
- 超大响应被截断并标注 `[truncated]`

## 边界与易错点

- 禁止 eval、禁止 exec；`%` 仅对整数取模；数字上限默认 1e15；除零返回错误
- `http_fetch` 的 **DNS 解析结果也必须校验**（防 DNS rebinding：解析后检查 IP 是否私网）
- 每个工具都必须声明超时（默认 10s 或更短）；工具输出上限默认 8 KiB

## 备注（已定决策）

- 复用 F-59 的 `internal/httpx`，不要另起一套 HTTP 客户端
