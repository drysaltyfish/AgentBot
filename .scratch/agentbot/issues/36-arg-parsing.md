# 36 · 泛型参数解析

- **Feature**: F-43（P1）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 33
- **写域（建议）**: internal/tool/

## 目标

每个工具都手写 json.Unmarshal 加字段校验太啰嗦。

## 交付物

- `func ParseArgs[T any](raw json.RawMessage) (T, error)`：解码 + 必填校验
- 支持 tag：`arg:"name"` / `,required` / `,enum=a|b|c` / `,default=10`
- 结构化错误 `type ArgError struct { Field, Rule, Got string }`，便于回灌给模型自我纠正

## 验收

- 表驱动：必填缺失、枚举非法、默认值生效、类型宽松转换、多字段同时出错
- 参数缺失且必填时，错误信息中列出**所有**缺失字段（不是只报第一个）

## 边界与易错点

- 未知字段默认忽略（兼容模型多传），可通过 tag 开启严格模式报错
- 类型不匹配（模型传字符串给整型字段）→ 尝试宽松转换后仍失败才报错


## Comments

### 2026-10-03 · 完成记录

**交付物**：`internal/tool/args.go`、`internal/tool/args_test.go`（8 个用例）

- `ParseArgs[T](raw)`：解码 + 必填/枚举/默认值校验，tag 为 `arg:"name,required,enum=a|b|c,default=10"`
- `ArgError{Field, Rule, Got}` + `ArgErrors` 汇总类型
- 类型不匹配时先做宽松转换（模型把 5 写成 "5"、true 写成 "true" 很常见），仍失败才报错
- 未知字段默认忽略；`ParseArgsStrict` 下报错

**"一次报全部问题"是刻意的**：遇到第一个就返回会让模型改一轮、再错一轮。测试断言
两个必填同时缺失时，错误里必须同时出现两个字段名。

**一处偏离规格**：规格写"可通过 tag 开启严格模式"。Go 没有结构体级 tag，
用字段级标记（如一个空的 marker 字段）只会给参数结构体添噪音，因此改为独立函数
`ParseArgsStrict`。语义更清楚，也避免每个工具结构体都挂一个占位字段。

**验收执行**：`go test -count=1 ./internal/tool/` → ok；全 18 包绿；lint 0 issues。
覆盖：默认值生效、枚举拒绝并精确点名、宽松转换（int/bool/float）、类型不匹配报字段名与期望类型、
未知字段两种模式、`arg:"-"` 忽略、重命名字段、非对象/非结构体入参。
