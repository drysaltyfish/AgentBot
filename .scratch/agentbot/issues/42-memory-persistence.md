# 42 · 记忆落盘与作用域隔离

- **Feature**: F-47 的"可选 JSONL 文件落盘" + 作用域隔离（M2 最小可用版本的必要修正）
- **里程碑**: M2（F-47 完整实现在 M3）
- **Status**: resolved
- **Blocked by**: 35
- **写域（建议）**: internal/agent/, internal/history/, internal/config/, cmd/server/

## 目标

M2 的最小记忆实现有两个问题，都必须在它能长期运行之前解决：

1. **不落盘**：纯进程内，重启即丢
2. **不隔离**：全局一份，任意会话存的内容会被注入任意会话——隐私缺陷

## 交付物

- `agent.HistoryMemory`：把记忆落在 `history.History` 上，复用 F-38 的 JSONL 存储
  （编码/权限/裁剪/并发都已测试过，不另写一套文件格式）
- 作用域经 ctx 传递：`WithMemoryScope` / `MemoryScopeFrom`，会话键即作用域
- `config.Agent.MemoryFile`：落盘路径；留空则仅进程内并打 WARN
- `history.File.WithTrimmer`：让文件存储也能用高水位批量裁剪

## 验收

- 两个独立存储实例读同一文件（模拟重启）能读到先前写入的记忆
- 落盘实现同样按作用域隔离
- 未配置存储时明确报错，而不是静默无效

## Comments

### 2026-10-03 · 完成记录

**隔离缺陷是先用测试证明的**，不是看代码推断的。修复前那条断言（"换个作用域应该读不到"）直接失败：

```
--- FAIL: Test_F40_MemoryLeakDemonstration
    作用域隔离缺失：换一个作用域仍读到了 [群A的秘密]
```

修复后转为回归测试 `Test_F47_MemoryIsIsolatedByScope`。

**为什么作用域走 ctx 而不是加进接口**：工具的执行签名是 `Execute(ctx, args)`，
改签名会波及所有工具；而作用域本就是"这次调用属于谁"的上下文信息。
ctx 方案让 `Memory` 接口、`Tool` 接口都不用动。

**为什么落盘条目用 `KindMarker`**：它不是对话轮次。即便同一份文件被当作聊天历史读取，
`conversation.ToMessages` 也会跳过 marker，不会误入提示词。

**裁剪用高水位**（`history.HighWater`）：窗口式裁剪会在每次写入超限时都缩短记忆段，
让记忆块每轮都变、白白失效缓存。这与本轮缓存优化的结论一致。

**验收执行**：`go test -count=1 ./internal/agent/` → ok（36 个用例）；
19 包全绿；golangci-lint 0 issues。

**仍未做（属 M3 的 F-47/F-48）**：`Scope` 结构体与三种作用域种类（会话/用户/全局）、
`MemoryItem{ID,Title,CreatedAt,Score,Refs}`、`Forget`/`List`、书签式记忆。
当前作用域只等于会话键。
