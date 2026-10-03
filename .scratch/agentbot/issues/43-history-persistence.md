# 43 · 对话历史落盘、历史召回工具与私聊会话隔离

- **Feature**: F-38 的落盘选项 / F-44 新增工具 / F-21 粒度修正
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 41, 42
- **写域（建议）**: internal/tool/builtin/, internal/session/, internal/config/, cmd/server/

## 目标

1. 对话历史也要落盘（此前只有记忆落盘）
2. 提供工具让模型能随时召回历史
3. 修掉私聊会话串台

## 交付物

- `config.History.File`：对话历史 JSONL 落盘路径；留空则仅进程内并打 WARN
- `builtin.recall_history`：按关键词或最近若干条召回**当前会话**的历史
- `tool.WithScope` / `tool.ScopeFrom`：作用域助手下沉到 tool 包（builtin 需要，
  但不能反向依赖 agent）
- `session.Manager.KeyFor`：私聊不再与其它私聊共用会话

## 验收

- 重启后仍能查到之前的对话
- `recall_history` 只读当前会话，**取不到会话时必须失败**，绝不退化成读全部
- 不同用户的私聊不共用会话

## Comments

### 2026-10-03 · 完成记录

**① 历史落盘**：与记忆一样复用 F-38 的 `history.File`（JSONL），裁剪同用高水位。
两个进程外存储（历史与记忆）指向不同文件，互不干扰。

**② recall_history**：会话键从 `tool.ScopeFrom(ctx)` 取——工具签名固定为
`Execute(ctx, args)`，而"这次调用属于哪个会话"本就是上下文信息。
**取不到作用域时返回失败而不是读全部**：那会串台。测试专门守住这一点。
只召回 `KindUser`/`KindAssistant`；工具轮次与 marker 不算"聊过的内容"。
条数有硬上限（50），防止模型一次拉空历史。

**③ 私聊串台（隐私缺陷）**：`KeyFor` 在 PerGroup 下对私聊也返回 `UserID=0`，
于是**所有私聊共用一个会话**——A 的上下文与记忆会出现在 B 的私聊里。
F-21 的 PerGroup 语义是"群消息按群分桶"，私聊没有群可归，天然应按用户分桶。
已改为私聊包含 UserID，群聊保持 `UserID=0`（有回归测试）。

**注意（数据影响）**：此修改改变了私聊的会话键
（`10001:0:0` → `10001:0:20002`），
因此**旧键下已落盘的记忆/历史不会被新键读到**。当前是开发数据，直接重存即可；
生产环境的迁移方案留待 M3。

**验收执行**：`go test -count=1 ./...` → 全绿；golangci-lint 0 issues；
gofmt/build/vet 干净。


### 2026-10-03 · 补记：recall_history 曾是个摆设（真机验证时发现）

真机跑完才发现一个**接线层面的设计缺陷**，代码结构直接可判定：

```
main.go:429  hist = history.NewFile(path, histItems)   // 存储上限 = 40 条
main.go:456  asm  = conversation.New(...)              // MaxHistory 未设 -> 呈现存储里的全部
main.go:293  History: hist                             // recall_history 读的是同一个存储
```

三者叠加的后果是：**提示词里恰好就是存储里的全部内容，工具没有任何"窗口之外"可召回**。
它能返回的永远是已经出现在上下文里的东西——等于没做。

根因是我把**存储保留量**与**呈现窗口**当成了同一个数字（都用 `history_turns*2`）。

修法：
- 新增 `config.History.Retention`（默认 400）：**存储**保留量，远大于窗口
- 呈现窗口交回装配层（`conversation.Assembler.MaxHistory = history_turns*2`）
- 但窗口**按批量滑动**（margin = max/2）：起点对齐到 margin 的整数倍，
  因此每 margin/2 轮才移动一次。逐轮滑动的窗口会让前缀每轮都变，
  前缀缓存必然失效——这正是本轮缓存优化一直在防的事

两条测试守住：
- `Test_CacheFirst_WindowSlidesInBatches`：60 轮里窗口移动次数必须远小于轮数
- `Test_CacheFirst_StoreLargerThanWindow`：最旧的历史**不出现在提示词里**，
  但仍留在存储中等待被召回

顺带补上可观测性：`llm call` 日志新增 `tools` 字段（本轮调用了哪些工具）。
先前只有 `tool_calls` 计数，排查时看不出**到底调了什么**——真机验证时我就卡在这里。
