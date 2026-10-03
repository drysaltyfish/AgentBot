# 36 · 泛型参数解析

- **Feature**: F-43（P1）
- **里程碑**: M2
- **Status**: open
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
