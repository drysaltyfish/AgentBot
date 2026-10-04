package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MemoryTextLimit 是单条记忆的长度上限（字符）。
// 取值对齐 F-47 的"单条记忆长度上限（默认 500 字符）"。
const MemoryTextLimit = 500

type memorySave struct{ deps Deps }

func (memorySave) Name() string { return "memory_save" }
func (memorySave) Description() string {
	return "把一条值得长期记住的信息写进记忆，必须是单行文本；" +
		"记忆按会话共享，因此内容里要写清这条事实属于谁（用发言人的昵称或 QQ 号作主语）"
}
func (memorySave) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"text": {Type: "string", Description: "要记住的内容，必须单行；写清属于谁，例如「张三很怕辣」"},
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
	return "读取已保存的长期记忆；默认返回本会话（群）的全部记忆。" +
		"只想了解某个人时，传 subject_qq 只看他的——回答关于某个人的问题前应先这样做，" +
		"否则会把别人的事当成他的"
}
func (memoryRecall) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"subject_qq": {
				Type: "integer",
				Description: "可选：只返回关于这个 QQ 号的记忆（不含未指明归属的公共记忆之外的他人记忆）。" +
					"不传则返回整个会话的记忆",
			},
		},
	}
}
func (memoryRecall) ReadOnly() bool        { return true }
func (memoryRecall) ConcurrencySafe() bool { return true }

type memoryRecallArgs struct {
	// SubjectQQ 可选：只看某个人（QQ 号）的记忆。
	SubjectQQ int64 `arg:"subject_qq"`
}

func (t memoryRecall) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, perr := tool.ParseArgs[memoryRecallArgs](args)
	if perr != nil {
		return tool.Failure(perr.Error()), nil
	}
	if t.deps.Memory == nil {
		return tool.Failure("记忆功能未配置"), nil
	}
	var (
		items []string
		err   error
	)
	// 传了 subject_qq 就走按人过滤；实现不支持时退化为全量召回，
	// 并在输出里说明——静默忽略参数会让模型以为自己看的是"只看这个人"。
	if in.SubjectQQ > 0 {
		if scopedMem, ok := t.deps.Memory.(agent.SubjectScopedMemory); ok {
			items, err = scopedMem.RecallFor(ctx, in.SubjectQQ)
		} else {
			items, err = t.deps.Memory.Recall(ctx)
			if err == nil {
				return tool.Success("（记忆实现不支持按人过滤，以下为全量）\n" +
					truncateOutput(strings.Join(items, "\n"))), nil
			}
		}
	} else {
		items, err = t.deps.Memory.Recall(ctx)
	}
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if len(items) == 0 {
		if in.SubjectQQ > 0 {
			return tool.Success("（没有关于这个人的记忆）"), nil
		}
		return tool.Success("（还没有任何记忆）"), nil
	}
	return tool.Success(truncateOutput(strings.Join(items, "\n"))), nil
}
