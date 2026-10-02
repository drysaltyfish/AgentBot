# 07 · F-01 双轨事件模型

- **Feature**: F-01（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/event/

## 目标

业务用归一字段、扩展用原始包，两者同时成立。

## 交付物

- `Event{Kind, Sub, SubSub, SelfID, UserID, GroupID, MessageID, Time, Sender, Raw json.RawMessage, RawTruncated bool}`
- `Kind` 由 `post_type` 映射；`Sub` 由 `message_type`/`notice_type`/`request_type` 映射；`message_sent` 归一为 `message`
- `func (e *Event) Get(path string) (any, bool)`：从 `Raw` 按 JSON Path 取值

## 验收

- 6 种平台样例 JSON 的 `Kind`/`Sub` 映射符合表驱动期望
- `message_sent` 样例的 `Kind` 等于 `message`
- `Get("sender.card")` 取到值；不存在路径返回 `false` 且不 panic
- `Raw` 超 1 MiB 时截断并置 `RawTruncated=true`

## 备注（已定决策）

- `Raw` 在首次 `Get` 时惰性解析一次并缓存
- 禁止 `unsafe` 零拷贝字符串指向网络缓冲

## Comments

### 2026-10-02 · 完成记录

实测：`internal/event` 的 `Event` + 单测；6 种平台样例的 Kind/Sub/SubSub 表驱动全过；`message_sent` 归一为 `message`；`Get("sender.card")` 命中、缺失路径返回 false；Raw 为空/非法 JSON 不 panic 且记录告警；超 1 MiB 截断置位；Raw 为拷贝不别名调用方缓冲。
