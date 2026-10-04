package main

import (
	"context"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/agent"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/reflect"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// defaultReflectWindow 是判断"滑出窗口"的兜底窗口大小。
const defaultReflectWindow = 40

// historyReflectSource 把历史存储与记忆写入适配成 reflect.Source。
//
// 水位用**条目下标**表达，而不是数据库 id：History 接口没有 id 概念，
// 而 reflect.Source 只要求"能判断哪些是新的"；用"已处理到第 N 条"既够用，
// 也不必为了水位去改一个被多处依赖的接口。
type historyReflectSource struct {
	hist     history.History
	mem      agent.Memory
	sessions *session.Manager
}

// Pending 返回水位之后、且**已滑出呈现窗口**的条目。
//
// 只取滑出窗口的那部分，既省钱也语义正确：还在窗口里的内容模型自己看得见，
// 让反思重复处理它们等于花两份钱。
func (s *historyReflectSource) Pending(ctx context.Context, sessionKey string, since int64, window int) ([]reflect.Item, error) {
	items, err := s.hist.Messages(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	if window <= 0 {
		window = defaultReflectWindow
	}
	slid := len(items) - window
	if slid <= 0 {
		return nil, nil
	}
	out := make([]reflect.Item, 0, slid)
	for i := 0; i < slid; i++ {
		// 水位是 1-based 的"已处理到第几条"；0 表示从未反思。
		if int64(i)+1 <= since {
			continue
		}
		it := items[i]
		if it.Kind != history.KindUser && it.Kind != history.KindAssistant {
			continue
		}
		if strings.TrimSpace(it.Content) == "" {
			continue
		}
		out = append(out, reflect.Item{
			Seq:         int64(i) + 1,
			SpeakerID:   it.SpeakerID,
			SpeakerName: it.SpeakerName,
			Text:        it.Content,
			Ambient:     it.Ambient,
			At:          it.At,
		})
	}
	return out, nil
}

// Save 写入一条反思结果。归属由 ctx 携带（调度器已放好），这里只转发。
func (s *historyReflectSource) Save(ctx context.Context, _ string, _ int64, text string) error {
	return s.mem.Save(ctx, text)
}

// Sessions 返回当前活跃会话的键（用于扫描）。
func (s *historyReflectSource) Sessions() []string {
	if s.sessions == nil {
		return nil
	}
	keys := s.sessions.Keys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.String())
	}
	return out
}

// buildReflection 构造反思调度器；未启用或客户端不可用时返回 nil。
//
// 反思会**主动花钱**，因此默认关闭；开启后仍受四道闸门约束（见 internal/reflect）。
// 模型走**关闭思考**的判官客户端——后台批处理不需要强模型，这是省钱的关键一步。
func buildReflection(cfg *config.Config, hist history.History, mem agent.Memory,
	sessions *session.Manager, lg *observe.Logger) *reflect.Scheduler {
	if !cfg.Agent.Reflect.EffectiveEnabled() || hist == nil || mem == nil {
		return nil
	}
	model, err := buildJudgeLLM(cfg, lg)
	if err != nil || model == nil {
		lg.Component("reflect").Warn("reflection client unavailable; idle reflection stays off", "error", err)
		return nil
	}
	rlog := lg.Component("reflect")
	reflector := memory.NewReflector(memory.ReflectOptions{
		Model:       model,
		ModelName:   cfg.LLM.Model,
		Timeout:     cfg.Agent.Reflect.EffectiveTimeout(),
		MaxItems:    cfg.Agent.Reflect.EffectiveMaxItems(),
		MaxFacts:    cfg.Agent.Reflect.EffectiveMaxFacts(),
		DailyBudget: cfg.Agent.Reflect.EffectiveDailyBudget(),
		Warn:        func(msg string) { rlog.Warn(msg) },
		Now:         time.Now,
	})
	src := &historyReflectSource{hist: hist, mem: mem, sessions: sessions}

	return reflect.New(src, reflector, func(key string) (time.Time, bool) {
		if sessions == nil {
			return time.Time{}, false
		}
		parsed, ok := session.KeyFromString(key)
		if !ok {
			return time.Time{}, false
		}
		sess, ok := sessions.Get(parsed)
		if !ok {
			return time.Time{}, false
		}
		return sess.LastSeen(), true
	}, reflect.Options{
		Enabled:     true,
		Every:       cfg.Agent.Reflect.EffectiveEvery(),
		IdleAfter:   cfg.Agent.Reflect.EffectiveIdleAfter(),
		MinInterval: cfg.Agent.Reflect.EffectiveMinInterval(),
		Window:      cfg.LLM.EffectiveHistoryTurns() * 2,
		Log:         lg,
	})
}
