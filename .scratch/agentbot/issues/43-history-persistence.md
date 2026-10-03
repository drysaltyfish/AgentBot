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
（`3828937966:0:0` → `3828937966:0:3315793548`），
因此**旧键下已落盘的记忆/历史不会被新键读到**。当前是开发数据，直接重存即可；
生产环境的迁移方案留待 M3。

**验收执行**：`go test -count=1 ./...` → 全绿；golangci-lint 0 issues；
gofmt/build/vet 干净。
