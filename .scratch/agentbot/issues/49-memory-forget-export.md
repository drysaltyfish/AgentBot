# 49 · 记忆的遗忘、导出与留存

- **Feature**: F-88
- **里程碑**: M3
- **Status**: resolved
- **Blocked by**: 48
- **写域（建议）**: internal/store/, internal/agent/, internal/tool/builtin/

## 目标

补上记忆的删除、导出与淘汰——目前只能写不能删。

## 交付物

- @BT@Forget(scope, id)@BT@ / @BT@ForgetAll(scope)@BT@ / @BT@List(scope)@BT@
- 导出命令：按作用域导出 JSONL，字段与表一致
- 留存：每作用域默认 200 条，超出按 **LRU + 分值** 淘汰；淘汰有日志
- "忘记我"走**显式工具或命令**，不依赖模型记忆

## 验收

- 删除后检索与注入都不再出现该条
- 删除幂等：删不存在的 id 不报错
- 导出内容与 @BT@List@BT@ 一致，且不含跨作用域数据
- 超限后最旧且分值最低的被淘汰；新鲜高分条目不被低分新条目挤掉

## Comments

### 2026-10-03 · 完成记录

`Forget`/`ForgetScope`/`List`/`TrimMemories` 在 ticket 48 随表一起落地；本 ticket 补齐
**工具、导出与留存执行**。

**交付物**
- 工具 `forget_memory`：按 id 遗忘，或 `all=true` 清空本作用域
- 工具 `list_memories`：列出本作用域记住的事，每行带 id（供用户检视）
- `builtin.MemoryAdmin` 接口：与 `Memory`（写入/召回）分开——删除是危险操作，
  应当能被单独关闭或替换实现
- `--export-memories <path>`：导出**全部作用域**为 JSONL
- `memory.Options.MaxPerScope`：写入后顺手淘汰，作用域不会无界增长

**几个刻意的选择**
1. **`forget_memory` 既没给 id 也没给 all 时必须失败**。默认清空是最糟的行为——
   一次参数解析失误就会抹掉用户的全部记忆。有测试守住。
2. **导出跨作用域，因此它是维护命令而不是会话内能力**。会话内只能看见自己的作用域；
   `list_memories` 走 ctx 作用域，导出走全局，两者权限不同所以入口不同。
3. **删除幂等**：删不存在的 id 不算错误，但回答里要说明"没找到"，
   而不是让模型以为删成功了。静默成功的假象比报错更难发现。
4. **淘汰失败不影响写入的成功语义**，但会告警——留存是维护性动作，
   不该因为清不掉旧数据就否定这次写入。

**一个 Go 接口的坑**：不能把 `nil` 的 `*memory.Store` 直接塞进 `builtin.MemoryAdmin`——
那样接口不为 nil，工具会以为管理能力可用，调用时才炸。已用显式判断规避。

**验收执行**（全绿）
- forget：按 id 删除并把 id 传给底层；删不存在返回"没有找到"而非报错
- forget all：只在 `all=true` 时清空；缺参数时**拒绝执行且不清空**
- list：内容与 id 正确；limit 生效；空记忆给出可读提示
- 未配置 MemoryAdmin 时两个工具都**明确失败**而非静默无操作
- 留存：超过上限后压到上限，**淘汰最旧的**（分值相同按更新时间）
- 导出：跨 2 个作用域共 4 条；每行可解析且字段与内部表一致
- `List` 只返回本作用域
- 冒烟：`--export-memories` 在真实库上导出 4 条 / 2 个作用域
