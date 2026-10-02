# 08 · F-02 通用消息 ID

- **Feature**: F-02（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/event/

## 目标

一套下游接口同时服务数字 ID 平台与字符串 ID 平台。

## 交付物

- `type ID struct{ num int64; raw string }`，只经 `IDFromInt64`/`IDFromString` 构造
- `Int64()`/`String()`/`IsZero()`/`Equal(other ID) bool`
- `MarshalJSON` 自适应；`UnmarshalJSON` 同时接受 number 与 string

## 验收

- 表驱动：`"123"` → JSON `123`；`"abc"` → JSON `"abc"`；往返一致
- 同一字符串两次构造得到相等 ID；空串 `IsZero()==true`
- crc64 结果 `<= 0xffff_ffff` 时把高位段置 1，不落入真实数字 ID 区间

## 备注（已定决策）

- Q12 已定：必须做防重叠置位（原规格漏了）

## Comments

### 2026-10-02 · 完成记录

实测：`internal/event` 的 `ID` + 单测；`"123"`→JSON `123`、`"abc"`→JSON `"abc"` 往返一致；空串 IsZero；crc64 结果恒 `> 0xffff_ffff` 且为正（防与真实数字 ID 相撞）。
