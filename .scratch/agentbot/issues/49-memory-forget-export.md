# 49 · 记忆的遗忘、导出与留存

- **Feature**: F-88
- **里程碑**: M3
- **Status**: open
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
