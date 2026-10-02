# 11 · F-05 调用抽象 Caller

- **Feature**: F-05（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 无
- **写域（建议）**: internal/transport/

## 目标

业务代码要能"发消息/踢人/禁言"，但这些是平台 API，必须可装饰、可 mock。

## 交付物

- `Request{Action, Params, Echo}`、`Response{Status, Data, Message, Wording, RetCode, Echo}`、`Response.OK()`
- `Caller` 接口 + `Middleware func(Caller) Caller`
- 内置 `RecordingCaller`/`RateLimitedCaller`/`RetryCaller`
- 语义化封装 `SendGroupMsg`/`SendPrivateMsg`/`DeleteMsg`/`SetGroupBan`

## 验收

- 装饰两层后仍正确穿透并返回原始 `Response`
- ctx 超时时 `Call` 在超时时间内返回 error
- `Params` 为 nil 时按空对象发送，不 panic

## 备注（已定决策）

- `RecordingCaller` 只记录发出的消息 ID；撤回与触发映射已明确排除
- `FakeCaller` 记录所有 API 请求，供端到端契约测试

## Comments

### 2026-10-02 · 完成记录

实测：`internal/transport/caller.go` + 单测；两层中间件按声明顺序穿透并返回原始 Response；ctx 超时在预算内返回；nil Params 序列化时消失；`RecordingCaller` 记录消息 ID（Data 为空不 panic 且不记录）；`RateLimitedCaller` burst=3 时放行 3 次、拒绝 2 次且被拒的调用不会到达下游；`SendGroupMsg`/`SendPrivateMsg`/`DeleteMsg`/`SetGroupBan` 动作与参数正确。撤回与触发映射按规格明确排除。
