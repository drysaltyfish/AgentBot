# 19 · F-13 三段中间件钩子

- **Feature**: F-13（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 17
- **写域（建议）**: internal/router/

## 目标

分群开关、限速、反并发、统计等横切关注点不应写进每个插件。

## 交付物

- `Engine{pre []Rule, mid []Rule, post []Handler}`
- 执行顺序：pre → 路由 Rules → mid → Handlers → post
- 钩子按注册顺序执行；`pre`/`mid` 返回 false 表示放弃本条路由、继续下一条
- 注册 API 统一：`engine.UsePre/UseMid/UsePost`；`Route.UsePre(...)`

## 验收

- 注册 pre（拒绝某群）、mid（同用户第二次拒绝）、post（计数），断言三者顺序与效果
- Handler panic 时 post 仍执行（defer 保证）
- pre 拒绝可观测（计数 + 可选日志）

## 备注（已定决策）

- 钩子执行必须受同一个 ctx 控制；不要把 pre 写在 match 内部

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/engine.go` + `engine_test.go`。钩子顺序断言为 `pre,pre-route,rules,mid,handler,post`；pre 拒绝时处理器不执行且拒绝可观测（`WithRejectHandler`）；mid 拒绝同样拦截；**Handler panic 后 post 仍执行**且 panic 被上报；一条路由的 Rule panic 不影响后续路由执行；`Once` 路由执行后自动从路由表移除且第二次不再执行；`Block` 阻断后续路由；`Break` 跳过 post；Caller 正确注入 `Ctx`。**偏离说明**：规格未定义 `Block`/`Break` 语义，实现定为：`Block` = 本条路由执行后停止尝试后续路由；`Break` = 同 `Block` 且跳过 post 钩子。
