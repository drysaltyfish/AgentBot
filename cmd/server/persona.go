package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/drysaltyfish/agentbot/internal/admin"
	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/conversation"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/scoped"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// personaStoreAdapter 把持久层的会话人格读写适配成 scoped.PersonaStore（F-82）。
//
// 适配层放在组合根而不是 scoped 或 store：两边都不必知道对方的存在，
// store 只管 SQL，scoped 只管"当前人格是谁"。
type personaStoreAdapter struct{ st *store.Store }

func (a personaStoreAdapter) Persona(ctx context.Context, ref scoped.SessionRef) (string, bool, error) {
	if a.st == nil {
		return "", false, nil
	}
	return a.st.GetPersona(ctx, ref.String())
}

func (a personaStoreAdapter) SetPersona(ctx context.Context, ref scoped.SessionRef, persona string) error {
	if a.st == nil {
		return fmt.Errorf("人格持久层不可用")
	}
	return a.st.SetPersona(ctx, ref.String(), persona)
}

// buildPersonas 加载人格定义、校验配置引用，并接上持久层（F-82）。
//
// 配置引用不存在的人格在启动期失败（F-25 fail-fast）：人格名写错要当场知道，
// 而不是等某个用户恰好切到它。
func buildPersonas(cfg *config.Config, persist scoped.PersonaStore, lg *observe.Logger) (*scoped.Registry, *scoped.Manager, error) {
	reg, err := scoped.Load(cfg.Prompt.EffectivePersonasDir())
	if err != nil {
		return nil, nil, err
	}
	configured := strings.TrimSpace(stringOr(cfg.Prompt.Persona, ""))
	if configured != "" {
		if err := reg.ValidateRefs(configured); err != nil {
			return nil, nil, err
		}
	}
	// 配置分层："全局默认 + 人格覆盖"（F-82）。人格文件里的 config 段写进
	// 各自的人格作用域，全局默认值写在空作用域。
	layers := scoped.NewConfig()
	reg.Apply(layers)
	if configured != "" {
		layers.Set("", scoped.KeyPersona, configured)
	}
	mgr, err := scoped.NewManager(layers, reg, persist, lg.Component("persona"))
	if err != nil {
		return nil, nil, err
	}
	lg.Info("personas loaded",
		"dir", cfg.Prompt.EffectivePersonasDir(), "count", reg.Len(), "configured", configured)
	return reg, mgr, nil
}

// personaHalfStatic 返回 F-65 半静态段的正文：当前会话人格的 system prompt。
//
// 返回**只有正文**，不含 RouteKey/Fingerprint：那些每会话不同，写进正文会让
// 同一人格的不同会话互相打碎前缀缓存。键与指纹只进日志与指标。
func personaHalfStatic(reg *scoped.Registry, mgr *scoped.Manager, warn func(string)) func(context.Context, session.Key) string {
	return func(ctx context.Context, key session.Key) string {
		if reg == nil || mgr == nil {
			return ""
		}
		name, err := mgr.Persona(ctx, scoped.SessionRefForKey(key))
		if err != nil {
			if warn != nil {
				warn(fmt.Sprintf("无法解析会话人格，本轮退化为无半静态段: %v", err))
			}
			return ""
		}
		if name == scoped.DefaultPersona {
			// DefaultPersona 是"未选择人格"的哨兵：它的设定由静态段的全局系统提示词
			// 承担。内置 default.yml 的正文与 conversation.DefaultSystemPrompt 逐字相同，
			// 若在这里再注入一次，每个请求都会把同一段话发两遍——那不是半静态段，
			// 是把缓存与 token 都花在重复上。
			return ""
		}
		p, ok := reg.Get(name)
		if !ok {
			return ""
		}
		return p.SystemPrompt
	}
}

// registerPersonaCommand 注册 /persona <名字>（F-82 的人格切换入口）。
//
// 切换只改持久化的作用域键，不重建会话（F-82 规格），因此下一轮请求的半静态
// 段就换成了新人格——这正是验收里"切换人格后哈希变化"的端到端形态。
func registerPersonaCommand(m *admin.Module, mgr *scoped.Manager, selfID int64) error {
	if m == nil || mgr == nil {
		return nil
	}
	return m.Register("persona", "/persona <名字> —— 切换本会话人格", func(ctx context.Context, inv admin.Invocation) (string, error) {
		if len(inv.Args) != 1 {
			return "", fmt.Errorf("用法：/persona <名字>: %w", admin.ErrUsage)
		}
		key := session.Key{SelfID: selfID, GroupID: inv.GroupID, UserID: inv.UserID}
		changed, err := mgr.SetPersona(ctx, scoped.SessionRefForKey(key), inv.Args[0])
		if err != nil {
			return "", err
		}
		if !changed {
			return "本会话的人格已经是 " + inv.Args[0] + "，未变化。", nil
		}
		return "已把本会话人格切到 " + inv.Args[0] + "，提示词半静态段随之下一次变化。", nil
	})
}

// personaPromptHash 构造 /prompt-hash 的数据源（F-65）。
//
// 半静态段按**调用者所在会话**计算：人格是会话级的，报告一个全局值只会
// 让运维对着一个与实际请求不符的哈希找问题。
func personaPromptHash(asm *conversation.Assembler, mgr *scoped.Manager, mem agent.Memory, selfID int64, lg *observe.Logger) admin.PromptHashFunc {
	if asm == nil {
		return nil
	}
	return func(ctx context.Context, inv admin.Invocation) ([]admin.HashSegment, error) {
		key := session.Key{SelfID: selfID, GroupID: inv.GroupID, UserID: inv.UserID}
		memoryBlock := ""
		if mem != nil {
			items, err := mem.Recall(agent.WithMemoryScope(ctx, key.String()))
			if err != nil {
				lg.Component("admin").Warn("prompt-hash: 记忆召回失败，半静态段不含记忆", "error", err)
			} else {
				memoryBlock = agent.RenderMemory(items)
			}
		}
		segs := asm.Segments(ctx, key, memoryBlock)
		out := make([]admin.HashSegment, 0, len(segs)+1)
		for _, s := range segs {
			out = append(out, admin.HashSegment{Name: s.Name, Hash: s.Hash})
		}
		// 路由键指纹不是提示词的一部分，但它决定"同人格的哪些会话共享前缀"，
		// 排查时第一眼要看的就是它。
		if mgr != nil {
			if fp, err := mgr.Fingerprint(ctx, scoped.SessionRefForKey(key)); err == nil {
				out = append(out, admin.HashSegment{Name: "persona-key", Hash: fp})
			}
		}
		return out, nil
	}
}
