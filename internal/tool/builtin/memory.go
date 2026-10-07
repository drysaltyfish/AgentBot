package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/scope"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MemoryTextLimit 是单条记忆的长度上限（字符）。
// 取值对齐 F-47 的"单条记忆长度上限（默认 500 字符）"。
const MemoryTextLimit = 500

type memorySave struct{ deps Deps }

func (memorySave) Name() string { return "memory_save" }
func (memorySave) Description() string {
	return "把一条值得长期记住的信息写进记忆，必须是单行文本。" +
		"先判断这条事实**关于谁**：关于当前说话的人就照写（如「张三很怕辣」）；" +
		"属于大家共同的事（活动通知、群规、共同决定）就把 shared 设为 true——" +
		"那种信息不该记成某一个人的。" +
		"不要记昵称、称呼、姓名、外号这类信息：那是平台名片的职责，" +
		"每条消息都带着当前名片，写进记忆只会留下一份会过期的副本。"
}
func (memorySave) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"text": {Type: "string", Description: "要记住的内容，必须单行；写清属于谁，例如「张三很怕辣」"},
			"shared": {
				Type: "boolean",
				Description: "true 表示这是**公共记忆**（活动安排、群规、大家共同的事），" +
					"不属于任何个人。默认 false = 关于当前发言人",
			},
		},
		Required: []string{"text"},
	}
}

type memorySaveArgs struct {
	Text string `arg:"text,required"`
	// Shared 为 true 时写成公共记忆（归属留空）。
	Shared bool `arg:"shared"`
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
	if in.Shared {
		// 公共记忆：显式清空归属，否则会被当成"当前说话人的事"。
		ctx = scope.WithoutSubject(ctx)
	}
	if err := t.deps.Memory.Save(ctx, text); err != nil {
		return tool.Failure(err.Error()), nil
	}
	if in.Shared {
		return tool.Success("ok（已记为公共记忆）"), nil
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
