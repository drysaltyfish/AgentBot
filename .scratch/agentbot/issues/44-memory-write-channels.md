# 44 · 记忆写入通道：从关键词触发改为模型自主判断

- **Feature**: F-48 的写入时机（两条通道）
- **里程碑**: M2
- **Status**: resolved
- **Blocked by**: 43
- **写域（建议）**: internal/agent/, internal/config/, cmd/server/

## 目标

F-48 规定写入时机为「模型显式调用 `save_memory`，或规则触发」——这是**两条**通道。
用户决定以"模型自己判断"为主，关闭关键词规则触发。

## 交付物

- `config.Agent.ProactiveMemory{Enabled, Instruction}`：在系统提示词末尾追加指令，
  要求模型遇到值得长期记住的事实就主动调用 `save_memory`（**默认开启**）
- `config.Agent.AutoMemory.Enabled` 改为普通 bool 且**默认关闭**
- `agent.ComposeSystemPrompt` / `agent.ProactiveMemoryInstruction` / `DefaultProactiveMemoryInstruction`

## 设计要点

**为什么追加在末尾**：人格部分原样保留在开头，指令独立成段；
但整段仍是**不可变前缀**（只随配置变化），前缀缓存不受影响。

**内置指令划了边界**：明确列出"该记"（称呼、稳定喜好、习惯、重要日期、承诺）
与"不该记"（寒暄、玩笑、临时状态、自己的话），并要求一次只记一条。
把噪声挡在存储之外，比事后清理便宜得多。

**这是一条概率性通道**：模型可能漏记也可能误记。规则触发是可审计的确定性通道，
两者独立，需要时可同时开启。

## 验收

- 提示词：人格部分在开头、指令在末尾、只出现一次、同样输入逐字节相同
- 关闭时提示词不含指令
- 自定义指令覆盖内置

## Comments

### 2026-10-03 · 完成记录

用户决定：关闭关键词触发（`auto_memory.enabled: false`），改用模型自主判断
（`proactive_memory.enabled: true`）。

关闭后 `WithMemoryCaptured` 标记不再设置，`save_memory` 恢复为正常通道——
该标记与同轮抑制逻辑保留，重新开启关键词触发时仍然有效。

上一条修复（同轮抑制 + 近似去重）与本改动互补：模型自主判断时**更容易**写出
近似重复的内容，所以那层相似度去重此时更重要。

验证：全包绿；golangci-lint 0 issues；gofmt/build/vet 干净。
