# 31 · F-55 统一出口过滤链

- **Feature**: F-55（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 11
- **写域（建议）**: internal/outbound/

## 目标

安全策略若散落在各发送点，新增一条发送路径就会漏掉。

## 交付物

- **唯一出口** `Sender.Send(ctx, target, msg)`；禁止旁路直接调底层 Caller 发送
- 固定顺序：长度/段数限制 → 敏感词替换 → 去噪 → `ReplaceTextOut` → 尾部剪裁 → 格式规范化 → 审计记录
- `type OutboundFilter func(string) string`，可插拔、每个 filter 可单独关闭

## 验收

- 所有发送路径都经过过滤链（用 `RecordingCaller` 校验内容已被处理）
- 注入 panic 的 filter 后仍能发送（recover + 放行原始内容 + 告警）
- 对同一内容连续过滤两次结果相同（幂等）
- 过滤后为空时不发送，但要记录审计

## 备注（已定决策）

- Q16 已定：补回 `ReplaceTextOut` 环节并对齐顺序
- 敏感词替换（F-56）在 M3；M1 先留出环节与接口

## Comments

### 2026-10-02 · 完成记录

实测：`internal/outbound/outbound.go` + 单测（10 项）。发送内容经真实 API 请求体断言：空行折叠与尾部裁剪确实生效，审计记录同时保留原始与过滤后文本；长度上限 20 时截断并带「已截断」标记；**注入 panic 的 filter 放行原始内容**且 `WithPanicHook` 被调用；固定链路顺序断言为 `length,sensitive,denoise,replace,trim,normalize`；单独关闭 denoise 后确实不执行；内置 filter 对 5 组样本（含超长、空串、中文）**幂等**；过滤后为空时不发送但写审计（`Dropped=true`）；空目标返回 `ErrNoTarget`；文本替换按固定键序执行；非文本段（at）原样透传。**修复了一个真实 bug**：`New()` 在应用 options 之后无条件重建 length filter，会把调用方自定义的 filter 覆盖掉；已改为只在使用方未提供时才用 maxLen 重建。**偏离说明**：去噪只做右侧裁剪与空行折叠，保留前导缩进（测试据此断言）。
