# 17 · F-10 Rule / Handler 分离

- **Feature**: F-10（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 14
- **写域（建议）**: internal/router/

## 目标

把"是否匹配"与"匹配后做什么"拆开，使匹配逻辑可复用、可单测。

## 交付物

- `type Rule func(*Ctx) bool`、`type Handler func(*Ctx)`
- 组合子 `And`/`Or`/`Not` 与 `R.All(rules...)`
- 集中定义 State 键常量：`StateKeyCommand`/`StateKeyArgs`/`StateKeyRegexMatch`/`StateKeyKeepPrefix`/`StateKeyImageURLs`

## 验收

- 组合子真值表测试（And/Or/Not 各 4 组输入）
- 一个 Rule 只判断、一个 Handler 只消费 State 的端到端用例
- 全仓库无裸 State 键字符串字面量

## 备注（已定决策）

- Rule 只判断与解析并写 `Ctx.State`；Handler 只做业务
- Rule 内不得起 goroutine；Rule panic 由调度层 recover

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/rule.go` 定义 `Rule`/`Handler` 与 `And`/`Or`/`Not`/`All` 组合子；真值表 13 组（含空入参的恒真/恒假）全过；`ctx_test.go` 中的端到端用例证明规则只写 `State`、处理器只读 `State`。
