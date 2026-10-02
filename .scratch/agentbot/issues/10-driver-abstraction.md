# 10 · F-04 传输抽象 Driver

- **Feature**: F-04（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 07
- **写域（建议）**: internal/transport/

## 目标

接正向 WS / 反向 WS / HTTP 三种上报；M1 只做 `wsclient` 与 `FakeDriver`。

## 交付物

- `Driver` 接口：`Connect(ctx) error`、`Listen(ctx, sink func(raw []byte, caller Caller)) error`
- `wsclient` 实现（`github.com/coder/websocket`，ISC）
- `FakeDriver` 内存事件管道，供契约测试
- `RetryDriver{next, backoff}`：1s 起、最长 30s、带 jitter

## 验收

- 内存管道：Connect 成功 → 投递 3 条事件 → cancel ctx → Listen 在 100ms 内返回
- Connect 失败返回非 nil error
- ctx 取消后不再调用 `sink`

## 备注（已定决策）

- `wsserver` 与 `http` 实现推到 M3
- `Connect` 必须返回 error，失败重试策略外置
- WS 写非并发安全，写必须加锁

## Comments

### 2026-10-02 · 完成记录

实测：`internal/transport/driver.go`（Driver/Sink/Closer/FakeDriver/RetryDriver）+ `ws.go` 的 `WSClient.Connect`；单测覆盖：FakeDriver 投递 3 条事件后 cancel → Listen 100ms 内返回且此后不再调用 sink；Connect 失败返回非 nil error；重连装饰器 2 次失败后成功（3 次尝试）。真实 `wsserver`/`http` 实现按计划推到 M3。
