# 21 · F-81 命令参数解析

- **Feature**: F-81（P1）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 20
- **写域（建议）**: internal/router/

## 目标

`/ban <id> [duration]` 这类命令需要把参数串变成结构化参数。

## 交付物

- `ParseCommandArgs(s) ([]string, error)`：shellwords 词法（单/双引号、反斜杠转义、空白折叠、未闭合引号报错）
- `BindFlags(v any, args []string) error`：按 `flag:"name,default=..."` tag 绑定到 `flag.FlagSet`
- `Command` 规则把参数切片写入 `StateKeyArgs`（类型 `[]string`）

## 验收

- 表驱动 6 组：带引号、带转义、未闭合引号、空串、重复空白、正常 flag 绑定
- `StateKeyArgs` 静态类型为 `[]string`
- 不支持的类型返回 error 而不 panic

## 备注（已定决策）

- 解析失败回一句用法提示（走 F-55 出口）后终止本条路由

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/command.go` + `command_test.go`。`ParseCommandArgs` 6 组：普通/双引号/单引号/反斜杠转义空格/空白折叠/引号未闭合（返回 `ErrUnterminatedQuote`）；空输入返回**非 nil 空切片**；`BindFlags` 覆盖 bool/int/int64/float64/string/time.Duration 六种类型并正确处理 default；未定义 flag 默认报错、`BindOptions{IgnoreUnknown:true}` 时忽略且不影响其它 flag；不支持的类型返回 `ErrUnsupportedFlagType`；非指针/空指针/非结构体返回 `ErrBadReceiver`；`Command` 规则把参数以 `[]string` 写入 `StateKeyArgs`。
