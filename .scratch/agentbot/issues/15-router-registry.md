# 15 · F-08 实例化路由注册表

- **Feature**: F-08（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 14
- **写域（建议）**: internal/router/

## 目标

插件式框架的核心，必须是实例而非全局变量。

## 交付物

- `Router{mu, routes []*Route, epoch}`；`Route{Kind, Rules, Handlers, Priority, Block, Break, Once, Name}`
- 链接式注册：`r.On("message", rules...).Priority(10).Handle(h)`
- 便捷触发器 `OnMessage/OnNotice/OnRequest/OnCommand/OnPrefix/OnSuffix/OnRegex/OnKeyword/OnFullMatch/OnAtMe`
- `Routes() []RouteInfo` 自省

## 验收

- 两个 `Bot` 实例各自注册不同路由，互不干扰
- 并发注册 1000 条 + 并发匹配，`-race` 通过
- `Name` 重复不报错但告警

## 备注（已定决策）

- **禁止包级全局注册表**；`Router` 由 `Bot` 持有并经依赖注入传递

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/router.go` + `router_test.go`。两个 Router 实例互不干扰；`Routes()` 自省字段完整；7 个便捷触发器都注册为 `message` 路由；`KindMatches` 表驱动含 `message_sent` 归一与 nil 事件。**修复了一个真实 bug**：`Named()` 在 `add()` 之后调用，导致重名计数永远为 0、告警不触发；已改为 `Named()` 也登记名字并在计数 1→2 时告警。**偏离说明**：规格同时要求字段 `Priority int` 与链式方法 `.Priority(10)`，二者同名冲突；实现用非导出 `priority`/`once` + 链式 `Priority()`/`Once()`，并提供 `Level()`/`IsOnce()` 访问器。
