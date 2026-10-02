# 03 · F-76 手写 Fake（M0：约定 + FakeClock）

- **Feature**: F-76（P0）
- **里程碑**: M0
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/testutil/

## 目标

无需 mock 框架即可做确定性测试。M0 只交付测试基建与 FakeClock；各接口的 fake 随其接口在 M1 落地。

## 交付物

- `internal/testutil/`：`FakeClock`（可控时间，支持前进/等待）
- 约定：所有 fake 并发安全、断言失败信息含"实际 vs 期望"完整内容
- 约定：每个 fake 与被 fake 的接口用编译期断言绑定

## 验收

- `internal/testutil` 自身单测不依赖网络与 sleep
- M1 各接口 fake 落地时编译期断言齐备

## 备注（已定决策）

- F-04/F-05/F-26 的 FakeDriver/FakeCaller/FakeLLM 由 10/11/23 号 ticket 交付

## Comments

### 2026-10-02 · 完成记录

实测：`internal/testutil/clock.go` + 单测；`go test ./internal/testutil` 通过。`FakeClock` 支持 Advance/Set/PendingWaiters，并发安全。
