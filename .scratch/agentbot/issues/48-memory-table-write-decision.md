# 48 · 记忆落库与写入决策

- **Feature**: F-87
- **里程碑**: M3
- **Status**: open
- **Blocked by**: 45
- **写域（建议）**: internal/store/, internal/agent/memory.go, internal/agent/automemory.go

## 目标

记忆从 JSONL 搬进 @BT@memories@BT@ 表，并把写入从"近似去重后追加"升级为
**新增 / 更新 / 忽略**的显式决策。

## 交付物

- @BT@memories@BT@ 表：@BT@scope_key / kind / title / text / source_refs / score / 内容指纹@BT@
- 写入决策函数：输入新事实 + 同作用域既有条目，输出 新增/更新/忽略 及**理由**
- 既有 Jaccard 去重保留为判据之一，但结论必须可解释（给出相似度与命中项）
- 排序确定性（同分按 id），保证注入渲染逐字节稳定
- 作用域参与**所有**读写路径

## 验收

- 同一事实不同措辞写两次只有一条；两个不同事实都保留
- 更新既有条目后注入顺序不变（就地更新）
- 跨作用域隔离有测试
- 内容指纹保证重放幂等
- 误合并对照用例：语义不同但共享词较多的两条不得被合并
