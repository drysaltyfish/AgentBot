package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/tool"
)

// ErrEndOfTurn 是 end_action 的控制流信号。
//
// 刻意不使用 io.EOF：后者的语义是"输入结束"，与网络错误混淆时很难排查（F-40 明确要求）。
// 约定：Run 返回 ErrEndOfTurn 表示"模型主动结束本轮"，**调用方不应发送任何消息**，
// 这也不属于失败。
var ErrEndOfTurn = errors.New("end of turn")

// 虚拟动作名。这三个名字是保留字，业务工具不得占用。
const (
	// ActionEndTurn 结束本轮，不回复。
	ActionEndTurn = "end_action"
	// ActionSaveMemory 写入长期记忆。
	ActionSaveMemory = "save_memory"
	// ActionNoop 显式空操作。
	ActionNoop = "noop"
)

// 控制信号：虚拟动作通过 Result.Metadata 把控制流意图传回循环。
const (
	// ControlMetadataKey 是控制信号的元数据键。
	ControlMetadataKey = "control"
	// ControlEndOfTurn 表示结束本轮。
	ControlEndOfTurn = "end_of_turn"
)

// VirtualAction 标记"由库内部执行、不需要宿主参与"的工具（F-40）。
//
// 与 ADR-0001 一致：虚拟动作就是注册表里的普通工具，其区别只在于执行者是库自己，
// 且结果会作为普通 observation 回灌，保持"调用→观察"循环完整。
type VirtualAction interface {
	tool.Tool
	// Virtual 返回 true，标记该工具由库内部执行。
	Virtual() bool
}

// IsVirtual 判断工具是否为虚拟动作。
func IsVirtual(t tool.Tool) bool {
	v, ok := t.(VirtualAction)
	return ok && v.Virtual()
}

// endTurnTool 实现 end_action。
type endTurnTool struct{}

func (endTurnTool) Name() string        { return ActionEndTurn }
func (endTurnTool) Description() string { return "本轮到此为止，不要再回复任何内容" }
func (endTurnTool) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{}}
}
func (endTurnTool) Virtual() bool  { return true }
func (endTurnTool) ReadOnly() bool { return true }
func (endTurnTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	return tool.Result{
		Output:   "ok",
		Metadata: map[string]string{ControlMetadataKey: ControlEndOfTurn},
	}, nil
}

// saveMemoryTool 实现 save_memory。
type saveMemoryTool struct{ mem Memory }

func (saveMemoryTool) Name() string { return ActionSaveMemory }
func (saveMemoryTool) Description() string {
	return "把一条值得长期记住的信息写进记忆（单行文本）"
}
func (saveMemoryTool) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"text": {Type: "string", Description: "要记住的内容，必须单行"},
		},
		Required: []string{"text"},
	}
}
func (saveMemoryTool) Virtual() bool { return true }

func (t saveMemoryTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if t.mem == nil {
		return tool.Failure("memory is not configured"), nil
	}
	// 本轮已由规则触发（用户明确说"记住：xxx"）写入过：不要再写一遍。
	// 实测过的事故：规则写入「我喜欢喝橙汁」后，模型又写了一条「用户喜欢喝橙汁」。
	if MemoryCaptured(ctx) {
		return tool.Success("本轮已根据用户的明确指令记录了这条记忆，无需重复保存"), nil
	}

	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		// 回灌明确的参数错误，让模型自己纠正，而不是中断循环。
		return tool.Failure(fmt.Sprintf("参数解析失败: %v", err)), nil
	}
	if err := t.mem.Save(ctx, in.Text); err != nil {
		return tool.Failure(err.Error()), nil
	}
	return tool.Success("ok"), nil
}

// noopTool 实现 noop。
type noopTool struct{}

func (noopTool) Name() string        { return ActionNoop }
func (noopTool) Description() string { return "什么都不做" }
func (noopTool) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{}}
}
func (noopTool) Virtual() bool  { return true }
func (noopTool) ReadOnly() bool { return true }
func (noopTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	return tool.Success("ok"), nil
}

// RegisterVirtual 把三个虚拟动作注册进注册表。
//
// mem 为 nil 时 save_memory 仍会注册，但执行时返回明确的失败原因（而不是让模型以为
// 自己写成功了）——静默失败是这套系统里最忌讳的。
func RegisterVirtual(r *tool.Registry, mem Memory) error {
	if r == nil {
		return errors.New("nil tool registry")
	}
	for _, t := range []tool.Tool{
		endTurnTool{},
		saveMemoryTool{mem: mem},
		noopTool{},
	} {
		if err := r.Register(t); err != nil {
			return fmt.Errorf("register virtual action %s: %w", t.Name(), err)
		}
	}
	return nil
}

// RenderMemory 把记忆渲染成注入用的文本块。
//
// 注入位置见 ADR-0002：system 之后、历史之前。渲染必须确定性——
// 同样的记忆内容两次渲染必须逐字节相同，否则会平白多失效一次缓存。
func RenderMemory(items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【你记得的事】")
	for _, it := range items {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(it))
	}
	return b.String()
}
