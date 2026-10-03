# fix-router 报告（task-7：C3 包内部分）

范围：仅 internal/router。未改动 cmd/server、internal/conversation、internal/agent（除写本报告）。

## 1. 删除未被调用的反射 flag 绑定（command.go）

删除：`BindFlags`、`BindFlagsOpt`、`BindOptions`、`filterKnownFlags`、`ErrBadReceiver`，
以及仅被它们使用的 `ErrUnsupportedFlagType`。command.go 从 183 行降到 61 行，
imports 从 9 个降到 3 个（errors/strings/unicode）。

保留：`ErrUnterminatedQuote`、`ParseCommandArgs`（Command() 依赖）。
command_test.go 删除 4 个 BindFlags 测试与 pingArgs 类型，保留 2 个 ParseCommandArgs 测试。

仓库级 grep 结果：`BindFlags|BindFlagsOpt|BindOptions|filterKnownFlags|ErrBadReceiver`
只出现在 command.go、command_test.go 和 FEATURES.md 文档中；cmd/server 零引用。
FEATURES.md:638 仍描述 BindFlags，属于文档（不在我的写作用域），需要 Lead/文档负责人同步删除。

## 2. Route 完全封装（router.go / engine.go）

`Route` 现在没有任何导出字段，全部可变态私有：

    kind rules handlers block brk name priority once pre expire created used removed owner

（brk 即原 Break 字段，避免关键词冲突。）

导出读面（all value/copy，无别名泄漏）：
- `Kind() string`、`Name() string`、`Level() int`
- `IsOnce() bool`、`IsBlocked() bool`、`SkipsPost() bool`
- `Rules() []Rule`（副本）、`Handlers() []Handler`（副本）、`PreRules() []Rule`（副本）
- `ExpiresAt() time.Time`、`Used() bool`、`Removed() bool`

导出链式构建方法（保持 Router 记账）：
- `Priority(p) *Route` → 触发 owner.markDirty()（重排序 + epoch++）
- `Named(name) *Route` → 触发 owner.noteName()（重名告警）
- `Once(on) *Route`、`Block(on) *Route`、`Break(on) *Route`、`Expire(d) *Route`
- `Handle(hs...) *Route`、`UseRules(rs...) *Route`、`UsePre(rs...) *Route`
  （追加型，engine 每次 Dispatch 直接读私有切片，无需 epoch；注册完成后再改写
  仍与并发 Dispatch 有竞争，沿用 FEATURES F-12 既有约定：快照存续期内 Route 视为只读。）

Router 内部与 engine.go 全部改为访问私有字段，热路径零分配：
engine 不再经过任何导出访问器/切片拷贝，`BenchmarkRouteMatch` 仍为 0 B/op, 0 allocs/op。

完全封装后，外部（含 cmd/server）无法再直接改写 Name/Block/Break/Kind/Rules/Handlers；
原测试里 `rt.Block = true`、`rt.Break = true`、`rt.Name` 已分别改为 `rt.Block(true)`、
`rt.Break(true)`、`rt.Name()`。行为（顺序、优先级、once/break、panic/reject、Kind 匹配）逐条保留。

## 3. 谓词规则 + 只记录 Always 路由

无需新增注册 helper：`OnMessage(rule).Handle(h)` 链已经能表达。新增黑盒示例
`internal/router/example_test.go`（package router_test）证明外部调用形态可编译并可运行。
Lead 可直接照抄：

    routes.OnMessage(replyRule(cfg)).Named("reply").Priority(router.PriorityEarly).Handle(replyHandler)
    routes.OnMessage(router.Always()).Named("record").Priority(router.PriorityLate).Handle(recordHandler)

谓词不命中时：reply 路由被 `reject(c, "rules")` 跳过，record 路由仍执行；engine.Dispatch
返回 1（只匹配 record）。命中时返回 2。注意：WithRejectHandler 会收到 phase="rules"
的拒绝回调，Lead 若要区分"回复策略未命中"需要在该回调里加白名单。

## 4. 新增测试

internal/router/route_test.go（4 个）：
- `Test_RouteReadSideAndBuilders`：读面/构建方法往返；
- `Test_RouteAccessorsReturnCopies`：Rules/Handlers/PreRules 返回副本，外部改写不泄漏；
- `Test_RoutePriorityBuilderReordersAfterRegistration`：注册后 Priority 仍触发重排序；
- `Test_PredicateRulePlusAlwaysRecordRoute`：C3 目标用法（谓词路由 + Always 记录路由 + reject 可观测）。

internal/router/example_test.go：黑盒 Example，Output 断言 dispatch 顺序与匹配数。

## 5. 执行的命令与结果

    go test ./internal/router/...            → ok  (baseline 亦通过)
    gofmt -l internal/router                 → 无输出
    go vet ./internal/router/...             → 无输出
    go test ./internal/router/... -count=1   → ok
    go test ./internal/router/... -race      → 环境不可用（-race requires cgo），已跳过
    go test ./internal/router/ -bench=BenchmarkRouteMatch -benchmem
        → 10690 ns/op, 0 B/op, 0 allocs/op（封装未引入热路径开销）

## 6. 刻意未改

- cmd/server/main.go：Lead 所有；只在最终消息给出可复制的注册示例。
- FEATURES.md:342/638 关于 Route 导出字段与 BindFlags 的描述已过时，属文档，未越界修改。
- 未新增 OnMessageFunc 之类 helper：`Handle` 链已覆盖，新增只会变成新的未调用导出面。
- 未对 UseRules/Handle 增加 markDirty：engine 不缓存这些切片，行为与改动前一致。
