package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MemoryAdmin 是记忆的遗忘与检视能力（F-88）。
//
// 与 Memory（写入/召回）分开：能写不等于能删，删除是**危险操作**，
// 应当能被单独关闭或替换实现。
type MemoryAdmin interface {
	// Forget 删除一条记忆，返回是否真的删掉了（幂等：删不存在的不算错误）。
	Forget(ctx context.Context, id int64) (bool, error)
	// ForgetScope 清空当前作用域，返回删除条数。
	ForgetScope(ctx context.Context) (int, error)
	// List 按更新时间倒序列出当前作用域的记忆。
	List(ctx context.Context, limit int) ([]store.Memory, error)
}

// ---- forget_memory ----

type forgetMemory struct{ deps Deps }

func (forgetMemory) Name() string { return "forget_memory" }
func (forgetMemory) Description() string {
	return "忘掉一条记忆（给 id），或忘掉本次会话的全部记忆（all=true）。只在对方明确要求忘记时使用"
}
func (forgetMemory) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{
		"id":  {Type: "integer", Description: "要忘掉的记忆 id（用 list_memories 查看）"},
		"all": {Type: "boolean", Description: "为 true 时忘掉本次会话的全部记忆；仅在对方明确要求时使用"},
	}}
}
func (forgetMemory) Dangerous() bool { return true }

type forgetMemoryArgs struct {
	ID  int64 `arg:"id"`
	All bool  `arg:"all"`
}

func (t forgetMemory) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[forgetMemoryArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if t.deps.MemoryAdmin == nil {
		return tool.Failure("记忆管理未配置"), nil
	}
	if in.All {
		n, err := t.deps.MemoryAdmin.ForgetScope(ctx)
		if err != nil {
			return tool.Failure(err.Error()), nil
		}
		return tool.Success(fmt.Sprintf("已忘掉本次会话的全部记忆（%d 条）", n)), nil
	}
	if in.ID <= 0 {
		return tool.Failure("需要给出 id，或把 all 设为 true"), nil
	}
	deleted, err := t.deps.MemoryAdmin.Forget(ctx, in.ID)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if !deleted {
		// 幂等：删不存在的 id 不算错误，但要让模型知道没删到东西。
		return tool.Success(fmt.Sprintf("没有找到 id=%d 的记忆（可能已经删过了）", in.ID)), nil
	}
	return tool.Success(fmt.Sprintf("已忘掉 id=%d 的记忆", in.ID)), nil
}

// ---- list_memories ----

type listMemories struct{ deps Deps }

func (listMemories) Name() string { return "list_memories" }
func (listMemories) Description() string {
	return "列出当前会话记住了哪些事，每行带 id"
}
func (listMemories) Parameters() tool.Schema {
	return tool.Schema{Properties: map[string]tool.Property{
		"limit": {Type: "integer", Description: "最多列出几条，默认 20，上限 50"},
	}}
}
func (listMemories) ReadOnly() bool        { return true }
func (listMemories) ConcurrencySafe() bool { return true }

type listMemoriesArgs struct {
	Limit int `arg:"limit"`
}

const (
	defaultListLimit = 20
	maxListLimit     = 50
)

func (t listMemories) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[listMemoriesArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if t.deps.MemoryAdmin == nil {
		return tool.Failure("记忆管理未配置"), nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	items, err := t.deps.MemoryAdmin.List(ctx, limit)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	if len(items) == 0 {
		return tool.Success("（还没有记住任何事）"), nil
	}
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "#%d %s", it.ID, it.Text)
	}
	return tool.Success(truncateOutput(b.String())), nil
}
