# 28 · F-38 对话历史管理

- **Feature**: F-38（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 23
- **写域（建议）**: internal/agent/

## 目标

历史是 LLM 应用的通用难点，应抽成可替换组件而非散落各处。

## 交付物

- `History` 接口：`Append`/`Messages`/`Reset`/`Trim`
- 条目类型：UserMessage/AssistantMessage/ToolCall/ToolResult/Marker
- `MemoryHistory`（环形缓冲，默认 50 条）；裁剪策略 `Window(n)`

## 验收

- 裁剪后不存在孤立的 tool 结果消息（配对完整性）
- 并发 Append 1000 条 + Messages，`-race` 通过
- `Messages` 返回副本；历史为空返回空切片而非 nil

## 备注（已定决策）

- M1 只实现 `Window(n)`，n=50（已定默认）；`TokenBudget`/`Summarize` 留接口
- 保留本规格的类型化条目（不改为 `expectResp bool`）

## Comments

### 2026-10-02 · 完成记录

实测：新建 `internal/history` 包（Memory + File/JSONL）+ 单测（10 项）。空历史返回**非 nil 空切片**；`Messages` 返回深拷贝（改返回值不影响内部，含 ToolCalls 切片）；`Window` **不拆散 tool_calls 与结果**——N=1 时宁可超出预算也保留配对，找不到 owner 的孤儿结果才丢弃；`Append` 超上限裁剪后无孤儿、无悬空 tool_calls；`Trim`/`Reset` 生效；8 goroutine × 125 次并发追加 + 200 次并发读取后条目数正好 1000 且无孤儿；ctx 取消被尊重；文件实现往返正确、按 key 隔离、`Trim`/`Reset` 不误伤其它 key。**偏离说明**：规格把 F-38 放在 `internal/agent`，这里独立成包，因为 session 与 agent 都要用它，放在任一方都会造成反向依赖。
