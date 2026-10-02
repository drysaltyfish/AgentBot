# 09 · F-03 消息段与消息链

- **Feature**: F-03（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 08
- **写域（建议）**: internal/event/

## 目标

一条消息是文本+图片+@+表情+回复的有序组合，不能被字符串拼接破坏结构。

## 交付物

- `Segment{Type string; Data map[string]string}`、`Message []Segment` 与类型常量
- 构造器 `Text/Image/At/Reply` 等
- `ParseMessage(raw) (Message, error)` 支持数组形态与 CQ 码字符串形态
- `Marshal()`、`PlainText()`、段内文本转义（CQ 形态额外转义逗号）

## 验收

- 数组形态与 CQ 码形态解析出相等的 `Message`
- 解析 → 序列化 → 再解析幂等
- 表驱动：空、纯文本、多段、含 at、含 image、含未知类型、含转义字符
- `Segment.String()` 对 base64 图片只输出长度+哈希前缀

## 备注（已定决策）

- 未知 `type` 必须原样保留（透传）
- CQ 码非法转义：跳过该段并记录，不整体失败

## Comments

### 2026-10-02 · 完成记录

实测：`internal/event` 的 `Message` + 单测；数组形态与 CQ 码形态解析结果 `reflect.DeepEqual` 相等；解析→序列化→再解析幂等；表驱动 7 组含未知类型与转义；非法 CQ 段跳过并告警而不整体失败；`Segment.String()` 对 base64 只输出长度+哈希前缀。
