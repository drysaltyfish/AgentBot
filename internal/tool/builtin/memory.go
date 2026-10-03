package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MemoryTextLimit 是单条记忆的长度上限（与 ADR-0002 的 2 KiB 一致）。
const MemoryTextLimit = 2048

type memorySave struct{ deps Deps }

func (memorySave) Name() string { return "memory_save" }
func (memorySave) Description() string {
	return "把一条值得长期记住的信息写进记忆，必须是单行文本"
}
func (memorySave) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"text": {Type: "string", Description: "要记住的内容，必须单行"},
		},
		Required: []string{"text"},
	}
}

type memorySaveArgs struct {
	Text string `arg:"text,required"`
}

func (t memorySave) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[memorySaveArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	// 参数层面的校验放在工具里，而不是只依赖 Memory 实现：
	// Memory 是接口，换个实现就可能丢掉这些约束。
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return tool.Failure("记忆内容不能为空"), nil
	}
	if strings.ContainsAny(text, "\r\n") {
		return tool.Failure("记忆必须是单行文本"), nil
	}
	if len([]rune(text)) > MemoryTextLimit {
		return tool.Failure("记忆内容超出长度上限"), nil
	}
	if t.deps.Memory == nil {
		return tool.Failure("记忆功能未配置"), nil
	}
	if err := t.deps.Memory.Save(ctx, text); err != nil {
		return tool.Failure(err.Error()), nil
	}
	return tool.Success("ok"), nil
}

type memoryRecall struct{ deps Deps }

func (memoryRecall) Name() string { return "memory_recall" }
func (memoryRecall) Description() string {
	return "读取已保存的长期记忆"
}
func (memoryRecall) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{}}
}
func (memoryRecall) ReadOnly() bool        { return true }
func (memoryRecall) ConcurrencySafe() bool { return true }

func (t memoryRecall) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if t.deps.Memory == nil {
		return tool.Failure("记忆功能未配置"), nil
	}
	items, err := t.deps.Memory.Recall(ctx)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if len(items) == 0 {
		return tool.Success("（还没有任何记忆）"), nil
	}
	return tool.Success(truncateOutput(strings.Join(items, "\n"))), nil
}
