// Package builtin 提供开箱即用的基础工具（FEATURES.md F-44）。
//
// 设计前提是**零注入面**：不 eval、不 exec、不读任意文件。计算器走 go/parser 的
// 白名单节点求值；http_fetch 复用 F-59 的 httpx（私网拒绝 + 每一跳校验 + 体积上限）。
package builtin

import (
	"context"
	"time"

	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MaxOutput 是单个工具输出的长度上限；超出会截断并标注。
const MaxOutput = 8 * 1024

// DefaultFetchTimeout 是 http_fetch 的超时。
const DefaultFetchTimeout = 10 * time.Second

// Memory 是内置记忆工具需要的存储能力。
//
// 刻意在这里重新声明（而不是 import agent 包）：Go 的接口是结构化的，
// agent.MemoryStore 天然满足它，而 builtin 不需要因此依赖 agent。
type Memory interface {
	Save(ctx context.Context, text string) error
	Recall(ctx context.Context) ([]string, error)
}

// Deps 是内置工具集的依赖。
type Deps struct {
	// Memory 为 nil 时 memory_save/memory_recall 会注册但执行时明确报错。
	Memory Memory
	// HTTP 是出站抓取配置；零值时使用 httpx.Defaults()。
	HTTP httpx.Config
	// History 供 recall_history 读取当前会话的历史；为 nil 时该工具会明确报错。
	History HistoryReader
	// Now 注入时间源（测试用）。
	Now func() time.Time
}

// Register 把内置工具集注册进注册表。
//
// 顺序固定为：calculator、current_time、json_query、http_fetch、memory_save、memory_recall。
// 顺序稳定直接关系前缀缓存（F-65），所以这里不依赖 map 遍历。
func Register(r *tool.Registry, deps Deps) error {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HTTP.MaxBytes == 0 {
		deps.HTTP = httpx.Defaults()
	}
	for _, t := range []tool.Tool{
		calculator{},
		currentTime{deps: deps},
		jsonQuery{},
		httpFetch{deps: deps},
		memorySave{deps: deps},
		memoryRecall{deps: deps},
		recallHistory{deps: deps},
	} {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// truncateOutput 按 rune 截断过长输出并标注。
func truncateOutput(s string) string {
	r := []rune(s)
	if len(r) <= MaxOutput {
		return s
	}
	return string(r[:MaxOutput]) + "\n[truncated]"
}
