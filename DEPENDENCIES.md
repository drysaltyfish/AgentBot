# 依赖准入清单

> 规则见 `FEATURES.md` §0.4。只接受 MIT / ISC / BSD-2-Clause / BSD-3-Clause / Apache-2.0。
> 下表每一行都来自**实际下载的模块源码**里核对过的许可证原文，不是文档或记忆。

| 用途 | 模块 | 版本（核对时） | 许可证（已核对） | 引入里程碑 |
|---|---|---|---|---|
| WebSocket 客户端（F-04） | `github.com/coder/websocket` | v1.8.15 | ISC | M1 |
| YAML 解析（F-25 / F-53 / F-82） | `gopkg.in/yaml.v3` | v3.0.1 | MIT + Apache-2.0 | M1 |
| goroutine 泄漏检测（F-70，仅测试依赖） | `go.uber.org/goleak` | v1.3.0 | MIT | M0 |
| SQLite（F-50） | `modernc.org/sqlite` | v1.60.1 | BSD-3-Clause（内嵌 public-domain SQLite、MIT 的 sqlite_vec） | M4 |

## 明确排除

- `golangci-lint`：工具链二进制，**不进** `go.mod`。
- 任何 GPL / AGPL / SSPL 或来源不明许可的模块。
- 任何 `pkg/errors` 类错误包装库（F-73 已定：错误包装一律用标准库）。

## 核对方法

```sh
go mod download -json <module>@<version>   # 取 Dir
# 读取 <Dir>/LICENSE*，确认许可证类型
```

核对日期：2026-10-02。
