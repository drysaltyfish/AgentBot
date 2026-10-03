# 37 · 内置安全工具集

- **Feature**: F-44（P1）
- **里程碑**: M2
- **Status**: resolved
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


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/tool/builtin/`（builtin.go / calc.go / text.go / net.go / memory.go / builtin_test.go，15 个用例）

`Register(r, Deps{Memory, HTTP, Now})` 按固定顺序注册六个工具：
calculator、current_time、json_query、http_fetch、memory_save、memory_recall。
顺序写死而不是遍历 map——顺序稳定直接关系前缀缓存（F-65）。

**零注入面怎么做到的**：计算器用 `go/parser` 解析后做**节点白名单**求值，
只允许字面量、二元、一元与括号。函数调用/标识符/下标等一律落到 default 被拒绝，
因此 `__import__("os")` 是在节点类型检查处被挡下的，而不是靠字符串黑名单。
测试覆盖了 `__import__("os")`、`1+`、`((1+2)*3`、`open("/etc/passwd")`、`1/0`、
浮点取余、`1e300*1e300` 七种，全部返回错误且不 panic。

**http_fetch 直接复用 F-59 的 httpx**（`Config.Get`），因此私网拒绝、DNS 解析结果校验
（防 rebinding）、每一跳复检、体积上限、超时全部继承。验收点 `127.0.0.1` 与
`169.254.169.254` 均已覆盖，另加了 `10.0.0.1`、`[::1]` 与 `file://`。

**json_query 用 UseNumber 解码**：`1234567890123456789` 的精度不能丢（F-39 的同类教训），
测试专门断言了这一点。

**参数校验放在工具里**（如 memory_save 的单行/非空/长度），而不是只依赖 Memory 实现：
Memory 是接口，换个实现就可能丢掉这些约束。

**本轮修掉一个真 bug**：`ParseArgs` 处理 `default=` 时把标签值当裸 JSON 注入，
导致**任何字符串默认值都会报参数错误**（`default=rfc3339` 解析失败）。
是被 builtin 的 current_time 测试抓出来的，已修为字符串默认值加引号（ticket 36 一并修正）。

**验收执行**：`go test -count=1 ./internal/tool/...` → ok（34 个用例）；18+1 包全绿；
golangci-lint 0 issues。

**未能覆盖**：超大响应的截断标注只在单元层面验证了 `truncateOutput`，
没有起一个真实大响应的 server 做端到端验证（httpx 侧已有 `ErrTooLarge` 与
`Response.Truncated` 的处理，属 F-59 的测试范围）。
