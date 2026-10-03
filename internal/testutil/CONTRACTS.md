# F-75 契约测试清单

规范来源：`FEATURES.md` F-75（第 2142 行）。本文件把五类契约、它们的测试入口，以及
"故意破坏什么会让哪条测试变红"登记在一处，避免契约测试散落后无人维护。

契约测试全部离线运行：只用 `httptest`（loopback）与手写 fake，不需要真实 API key，
不依赖外网。

| # | 契约 | 测试 | 位置 |
|---|---|---|---|
| 1 | 多轮工具调用契约 | `Test_F35_ToolProtocolContract` | `internal/agent/react_test.go` |
| 2 | 流式契约（聚合） | `Test_F75_StreamingContract` | `internal/testutil/contract_test.go` |
| 2 | 流式契约（取消关闭连接） | `Test_F75_StreamingCancelClosesConnection` | `internal/testutil/contract_test.go` |
| 3 | 一个动作端到端契约 | `Test_F75_OneActionEndToEndContract` | `internal/testutil/contract_test.go` |
| 4 | 传输契约（Connect/Listen/cancel） | `Test_F04_FakeDriverDeliversEventsAndListenReturnsOnCancel` | `internal/transport/driver_test.go` |
| 5 | 裁剪契约（无孤立 tool 结果） | `Test_F38_WindowKeepsToolCallPairIntact` / `Test_F38_TrimDropsUnrecoverableOrphan` | `internal/history/history_test.go` |

## 反例对照（F-75 验收：故意破坏后测试必须失败）

- 破坏 `tool_call_id` 回传（把 tool call 塞进 content、或不回传 id）→ 契约 1 红。
- 破坏流式聚合（丢弃分片、不发终止分片）→ 契约 2（聚合）红。
- 取消 `ctx` 后不关闭连接 / 生产者泄漏 → 契约 2（取消）红。
- 阻断 `Sender -> Caller` 的出口或丢失用户输入 → 契约 3 红。
- `Listen` 在 cancel 后不返回 / 关闭后仍调用 sink → 契约 4 红。
- 裁剪时拆散 assistant 的 `tool_calls` 与对应 tool 消息 → 契约 5 红。

## 已知边界

- 流式实现目前按 DeepSeek/OpenAI 的完整 `delta.tool_calls[]`（必须带 `id`）透传；
  按 `index` 的增量参数拼接属 F-29（M3），`internal/llm/openai.go` 有明确注释。
  因此契约 2 断言的是"带 id 的分片原样透传"，而不是把无 id 的参数碎片拼起来。
