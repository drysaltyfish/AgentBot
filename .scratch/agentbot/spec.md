# AgentBot · Spec（M0 + M1）

> **规范来源**：[`FEATURES.md`](../FEATURES.md)（v2，82 条 Feature）是**唯一规范**。
> 本文件只记录"本轮做什么、按什么约定做、以及已经拍板的决策"，不重复 Feature 细节。
> **本轮范围**：M0 骨架 + M1 最小闭环，共 **32** 张 ticket。

## 1. 本轮目标

| 里程碑 | 目标 | 完成判据 |
|---|---|---|
| **M0 · 骨架** | 立规矩，一行业务代码都不写 | CI 跑通 build/vet/lint/test -race；空 Bot 能优雅关闭且 goleak 干净；无 lint 违规 |
| **M1 · 最小闭环** | 群消息 → LLM → 回消息 跑通 | F-75 中"流式契约"与"单动作端到端"两条通过；真实群里能对话 |

**M0 与 M1 之间的纪律不可跳过。** 参考项目里绝大多数严重问题（全局态、无 ctx、无 race 检查）都源于"先跑起来再说"。

## 2. 已定决策（规格评审结论，实现时不得推翻）

| # | 决策 | 结论 |
|---|---|---|
| 1 | 仓库定位 | 自用应用 + 可复用内核；除 `cmd/server` 外全部放 `internal/`；不承诺公开 API 稳定性 |
| 2 | 模块路径 | `github.com/drysaltyfish/agentbot` |
| 3 | 许可 | MIT；仓库内不出现任何第三方项目的名称、致谢或来源说明 |
| 4 | 依赖准入 | 白名单：MIT / ISC / BSD-2 / BSD-3 / Apache-2.0；已批准清单见 [`DEPENDENCIES.md`](../DEPENDENCIES.md) |
| 5 | Go 版本 | 锁 `1.27.1`；`go.mod` / CI / `.golangci.yml` 三者一致；不用 `toolchain` 指令 |
| 6 | 配置格式 | YAML（`gopkg.in/yaml.v3`）；"未设置 vs 零值"一律用 **`*T` 指针**，不用 `Option[T]` |
| 7 | 错误约定 | 包装一律 `fmt.Errorf("...: %w", err)`；哨兵用包级 `var ErrXxx = errors.New(...)` + `errors.Is`；**禁 `pkg/errors`** |
| 8 | 传输鉴权 | fail-closed：`wsserver` 未配 Token 即**启动失败**；密钥比较用 `hmac.Equal` |
| 9 | 权限角色 | 由**库内** `Resolver` 推导；不接受宿主传入裸 role 字符串；失败落最低权限 |
| 10 | 工具执行归属 | Agent **内部执行**；`Output.ToolCalls` 是"已执行记录"而非待执行队列 |
| 11 | 持久化 | M1 只做进程内 map + 可选 JSONL；SQLite 推迟到 M4，用 `modernc.org/sqlite`（纯 Go） |
| 12 | 会话键 | `SessionKey{SelfID, GroupID, UserID}`；日志/指标里的 `session_key` 一律用它 `String()` |
| 13 | 传输范围 | M1 只做 `wsclient` + `FakeDriver`；`wsserver`/`http` 推到 M3 |
| 14 | 状态保留键 | 常量 `StateKeyKeepPrefix = "__keep__"`（前后各两个下划线） |
| 15 | 裁剪默认策略 | `Window(n)`，n=50；`TokenBudget`/`Summarize` 留接口，M3 再接 |
| 16 | 部署形态 | 单二进制 + 同目录配置；最小 Dockerfile；**不做** K8s/Helm/Makefile |
| 17 | 工程杂项 | 禁止代码生成；lint 只报不改（`issues.fix: false`）；发布自动化推到 M3 之后 |
| 18 | 里程碑校准 | F-15 提为 P0；F-07/F-22/F-48/F-50 移入 M3；F-52 移回 M4；M2 不再重复分配 F-11 |
| 19 | 提示词缓存 | "静态段"定义为**进程启动期内**逐字节稳定；权限表热加载会主动使其失效，属预期行为 |

完整理由与逐条裁决见 `FEATURES.md` §0.4、§12.1 与附录 A。

## 3. 包布局（M0/M1 落地范围）

```
AgentBot/
├── cmd/server/            组合根：读配置 → 装配 → 启动 → 优雅关闭
├── internal/
│   ├── config/            F-25
│   ├── observe/           F-67, F-70
│   ├── event/             F-01, F-02, F-03
│   ├── transport/         F-04, F-05, F-06, F-80
│   ├── router/            F-08~F-14, F-81（含 engine 三段钩子）
│   ├── session/           F-21
│   ├── llm/               F-26, F-28, F-30
│   ├── prompt/            F-33
│   ├── agent/             F-34, F-38, F-39
│   ├── policy/            F-53
│   ├── outbound/          F-55
│   ├── httpx/             F-59
│   ├── testutil/          F-76
│   └── unsafeutil/        F-73 唯一 unsafe 白名单目录
├── prompts/               F-33 模板 + F-82 人格（M3）
├── actions.yaml           F-53 权限表
├── testdata/              黄金文件（F-74）
└── deploy/                最小 Dockerfile
```

任何包若不在 `cmd/server` 的装配路径上，就不准进仓库（反模式 #7）。暂不接线的放 `_experimental/`。

## 4. 工程约定

- **测试命名**：`Test_F27_Backoff` 形式，Feature 编号进测试名（F-27 的例子见 `FEATURES.md` §0）。
- **fake 优先**：核心链路的单测不需要网络、不需要 sleep（除显式并发测试）。
- **契约测试**：M1 必须有的两类——流式契约、单动作端到端；多轮工具调用契约在 M2。
- **提交纪律**：一个 ticket 一个 PR；ticket 完成时把 `Status:` 改为 `resolved` 并记录验证命令。

## 5. ticket 索引

| # | Feature | 标题 | 优先级 | 里程碑 | 前置 | 文件 |
|---|---|---|---|---|---|---|
| 01 | F-73 | Lint 纪律 | P0 | M0 | 无 | `issues/01-lint-discipline.md` |
| 02 | F-78 | CI 流水线 | P0 | M0 | 01 | `issues/02-ci-pipeline.md` |
| 03 | F-76 | 手写 Fake（M0：约定 + FakeClock） | P0 | M0 | 无 | `issues/03-test-fakes.md` |
| 04 | F-25 | 配置校验与 fail-fast | P0 | M0 | 无 | `issues/04-config-validate.md` |
| 05 | F-67 | 结构化日志与追踪 | P0 | M0 | 04 | `issues/05-structured-logging.md` |
| 06 | F-70 | 优雅关闭（M0：骨架） | P0 | M0 | 04 | `issues/06-graceful-shutdown.md` |
| 07 | F-01 | 双轨事件模型 | P0 | M1 | 无 | `issues/07-event-model.md` |
| 08 | F-02 | 通用消息 ID | P0 | M1 | 无 | `issues/08-message-id.md` |
| 09 | F-03 | 消息段与消息链 | P0 | M1 | 08 | `issues/09-message-segments.md` |
| 10 | F-04 | 传输抽象 Driver | P0 | M1 | 07 | `issues/10-driver-abstraction.md` |
| 11 | F-05 | 调用抽象 Caller | P0 | M1 | 无 | `issues/11-caller-abstraction.md` |
| 12 | F-06 | 请求-响应关联（echo） | P0 | M1 | 10, 11 | `issues/12-echo-correlation.md` |
| 13 | F-80 | 传输鉴权 | P0 | M1 | 10 | `issues/13-transport-auth.md` |
| 14 | F-11 | 事件上下文与 State | P0 | M1 | 11 | `issues/14-ctx-state.md` |
| 15 | F-08 | 实例化路由注册表 | P0 | M1 | 14 | `issues/15-router-registry.md` |
| 16 | F-09 | 稳定优先级排序 | P0 | M1 | 15 | `issues/16-priority-order.md` |
| 17 | F-10 | Rule / Handler 分离 | P0 | M1 | 14 | `issues/17-rule-handler.md` |
| 18 | F-12 | 热路径快照匹配 | P0 | M1 | 15, 16 | `issues/18-snapshot-match.md` |
| 19 | F-13 | 三段中间件钩子 | P0 | M1 | 17 | `issues/19-engine-hooks.md` |
| 20 | F-14 | 内置规则库 | P0 | M1 | 17 | `issues/20-builtin-rules.md` |
| 21 | F-81 | 命令参数解析 | P1 | M1 | 20 | `issues/21-command-args.md` |
| 22 | F-21 | Session 与 Manager | P0 | M1 | 14 | `issues/22-session-manager.md` |
| 23 | F-26 | 统一 LLM 接口 | P0 | M1 | 无 | `issues/23-llm-interface.md` |
| 24 | F-28 | 流式契约 | P0 | M1 | 23 | `issues/24-stream-contract.md` |
| 25 | F-30 | 重试与退避 | P0 | M1 | 23 | `issues/25-retry-backoff.md` |
| 26 | F-33 | 提示词模板引擎 | P0 | M1 | 无 | `issues/26-prompt-templates.md` |
| 27 | F-34 | 统一 Agent 契约 | P0 | M1 | 23, 26 | `issues/27-agent-contract.md` |
| 28 | F-38 | 对话历史管理 | P0 | M1 | 23 | `issues/28-history.md` |
| 29 | F-39 | 动作流解析 | P0 | M1 | 无 | `issues/29-action-parse.md` |
| 30 | F-53 | 权限即提示词 | P0 | M1 | 26 | `issues/30-policy-prompt.md` |
| 31 | F-55 | 统一出口过滤链 | P0 | M1 | 11 | `issues/31-outbound-filter.md` |
| 32 | F-59 | 出站 HTTP 安全 | P0 | M1 | 无 | `issues/32-outbound-http.md` |
| 33 | F-41, F-42 | 工具注册表与自描述接口 | P0 | M2 | 无 | `issues/33-tool-registry.md` |
| 34 | F-35 | ReAct 循环 | P0 | M2 | 33 | `issues/34-react-loop.md` |
| 35 | F-40 | 虚拟动作闭环 | P1 | M2 | 34 | `issues/35-virtual-actions.md` |
| 36 | F-43 | 泛型参数解析 | P1 | M2 | 33 | `issues/36-arg-parsing.md` |
| 37 | F-44 | 内置安全工具集 | P1 | M2 | 33 | `issues/37-builtin-tools.md` |
| 38 | F-45 | 工具权限与人工审批 | P1 | M2 | 34 | `issues/38-tool-approval.md` |
| 39 | F-15 | 一次性 / 临时路由 | P0 | M2 | 无 | `issues/39-temp-routes.md` |
| 40 | F-16 | 交互式等待（Await/Stream） | P0 | M2 | 39 | `issues/40-await-stream.md` |
| 41 | F-35 等 | Agent 接入组合根（M2 收口） | P0 | M2 | 34, 37, 38, 39 | `issues/41-agent-wiring.md` |
| 42 | F-47（部分） | 记忆落盘与作用域隔离 | P0 | M2 | 35 | `issues/42-memory-persistence.md` |

M2 的 Feature 范围来自附录 A：F-15, F-16, F-35, F-40, F-41~F-45。
完成判据：Await 多轮对话可用 + F-75 中"多轮工具调用契约"通过。
**F-15（ticket 39）必须先于 F-16（ticket 40）完成。**

## 6. 已登记的分歧点

### 6.1 已裁定（2026-10-03）

| 编号 | 分歧点 | 裁定 | 记录 |
|---|---|---|---|
| G7 | F-35 的 `tool_calls` 管道与 F-39/F-40 的 Action 管道如何共存、是否合并为一条 | **原生 `tool_calls` 为唯一执行通道**；F-39 的解析器降级为抢救通道（scavenge）；F-40 的虚拟动作注册为普通工具；配置 `agent.protocol: native \| auto`，默认 `auto` | `docs/adr/0001-tool-call-protocol.md` |
| G8 | `save_memory` 的记忆注入位置——与"不可变前缀"的缓存设计正面冲突 | 记忆作为**独立消息放在 system 之后、历史之前**；长度上限 2 KiB；内容确定性排序 | `docs/adr/0002-memory-injection-position.md` |

G8 是本轮做前缀缓存优化时新发现的分歧点：把它并入 system 会让每次记忆更新都报废整块缓存。

### 6.2 尚未裁定

以下两处仍是真正的双向门，**在其所属 Feature 落地前必须单独确认**，不得默默二选一：

| 编号 | 冲突点 | 何时必须裁定 |
|---|---|---|
| G3 | F-55 出口过滤 × F-64 流式前缀差：逐增量过滤会让跨分片敏感词失效；整段过滤再做差分会把截断标记带进前缀差 | F-64 落地前（M3） |
| G5 | F-32 `Budget.Fit` 与 F-38 `TokenBudget` 谁先裁剪、pinned system 与配对完整性冲突时谁优先 | F-32 落地前（M3） |

其余跨 Feature 接线缺口按"谁先定义谁负责 + M1 链路优先"的规则在各自 Feature 内解决。

---

*生成日期：2026-10-02 · 对应 `FEATURES.md` v2*
