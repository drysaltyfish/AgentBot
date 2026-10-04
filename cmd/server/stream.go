package main

import (
	"context"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
)

// buildStreamSplitterFactory 构造"每次回复一个流式切分器"的工厂（F-64）。
//
// 返回 nil 表示未启用——调用方据此完全走原来的整段发送路径，不做任何分支猜测。
//
// 切分器必须每次新建并绑定 ctx 与目标：它持有"已发送前缀"状态，跨回复复用
// 会把上一轮的文本当成这一轮已发的内容，直接导致漏发。
func buildStreamSplitterFactory(cfg *config.Config, sender *outbound.Sender, lg *observe.Logger) func(context.Context, outbound.Target) *llm.StreamSplitter {
	if cfg == nil || sender == nil || !cfg.Stream.EffectiveEnabled() {
		return nil
	}
	scfg := cfg.Stream
	return func(ctx context.Context, target outbound.Target) *llm.StreamSplitter {
		ss := outbound.NewStreamSender(sender, target, outbound.StreamOptions{
			TypingHint:   scfg.TypingHint,
			EditMessages: scfg.EffectiveEditMessages(),
			OnHalt: func(reason string) {
				lg.Component("stream").Warn("incremental send halted", "reason", reason)
			},
		})
		return llm.NewStreamSplitter(llm.StreamConfig{
			MaxChars:      scfg.EffectiveMaxChars(),
			MaxInterval:   scfg.EffectiveMaxInterval(),
			MinInterval:   scfg.EffectiveMinInterval(),
			FirstMinChars: scfg.EffectiveFirstMinChars(),
			EditMessages:  scfg.EffectiveEditMessages(),
			Flush:         ss.Handler(ctx),
			OnWarn:        func(msg string) { lg.Component("stream").Warn(msg) },
		})
	}
}
