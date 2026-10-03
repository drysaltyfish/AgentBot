# 47 · 会话台账与用量归集

- **Feature**: F-85
- **里程碑**: M3
- **Status**: resolved
- **Blocked by**: 45
- **写域（建议）**: internal/store/, internal/llm/, cmd/server/

## 目标

把缓存命中率与 token 消耗从"日志里的数字"变成"可查询、可回归的数据"。

## 交付物

- @BT@sessions@BT@ 表：会话级 token 分类计数、请求数、工具调用数、估计成本、价格版本
- 每次 LLM 调用后**原子累加**（@BT@UPDATE ... SET x = x + ?@BT@），禁止读改写
- 查询：会话级与全局级的命中率、成本
- 价格表带版本号；老数据按当时版本可解释
- 写入失败**不影响回复主流程**，只告警

## 验收

- 单会话多轮后的 token 与请求数 = 各轮之和
- 并发调用同一会话累加无丢失（对照测试）
- 查询出的命中率与同轮日志打印值一致
- 用量写入失败时回复仍正常返回（注入失败的 store 做对照）

## Comments

### 2026-10-03 · 完成记录

**交付物**
- `sessions` 表：会话级请求数、工具调用数、四类 token、估计成本、价格版本
- `Store.AddUsage`：`INSERT ... ON CONFLICT DO UPDATE SET 列 = 列 + 增量`
- `Store.SessionUsage` / `UsageTotals` / `TopSessions` / `ResetUsage`
- `llm.Price` + `Price.Cost`：命中与未命中**分开计价**
- `agent.Output.LLMCalls`：运行实际发生的请求数（步数把思考与动作分开计，不是请求数）
- 配置 `llm.pricing.{version,input_per_million,output_per_million,cache_hit_per_million}`
- 组合根在每轮回复后累加用量；**失败只告警**，不影响回复
- `--stats` 命令：打印总量、缓存命中率、估计成本与花费最高的会话

**两个刻意的设计选择**
1. **累加在数据库里做**，不用"读出来加一加再写回"。后者在并发下会丢增量，
   而丢增量**不会报任何错**——只是数字慢慢变得不可信。有并发测试守住（16×25 无丢失）。
2. **消息数不设累加列**，由 `messages` 表实时统计。累加计数会与实际行数漂移，
   而这类漂移没人会发现。测试里删一条消息后计数立即变化即为证。

**价格表**：`CacheHitRatio` 的分母只用命中+未命中。供应商上报的 `PromptTokens` **含命中部分**，
拿它当分母会把命中率算小；计价同理，只用 hit/miss 两列可避免重复计费。
价格未配置时成本恒为 0，但用量照记——"花了多少 token"与"花了多少钱"是两件事。

**真跑才发现的 bug**：`UsageTotals` 在**空表**上崩溃——`MIN/MAX/SUM` 返回 NULL，
扫进 int64 直接报错。这在"新装环境第一次查台账"时必现，而单测如果只造了数据就永远撞不到。
已给全部聚合加 `COALESCE`，并补了空库测试。

**验收执行**（`go test -count=1 ./internal/store/` 全绿）
- 三次累加后各项等于三次之和；价格版本被记录
- **16 goroutine × 25 次并发累加**：请求数与 token 数均无丢失
- 会话之间互不串数据；不存在的会话返回 `ErrSessionNotFound`
- 全局总量与排行（按成本降序）正确
- 消息数随 `messages` 表实时变化
- 命中率边界：无 token → 0；全命中 → 1；分母为 hit+miss
- **空库**上查总量与排行不报错
- `--stats` 在真实数据库上可用（消息 42 条）

**未做**：F-85 提到的"会话归档时固化最终值"未实现——归档本身属于 F-88 的范围。
