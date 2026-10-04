package main

import (
	"context"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/outbound"
)

// Test_F64_BuildStreamSplitterFactoryFollowsConfig 覆盖流式接线的开关：
// 默认关闭时返回 nil（调用方完全走整段路径），开启后每次都能拿到新的切分器。
func Test_F64_BuildStreamSplitterFactoryFollowsConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	lg := testLogger(t)
	sender := outbound.NewSender(nil, outbound.New())

	if f := buildStreamSplitterFactory(cfg, sender, lg); f != nil {
		t.Fatal("默认关闭时不该构造工厂")
	}

	cfg.Stream.Enabled = ptr(true)
	f := buildStreamSplitterFactory(cfg, sender, lg)
	if f == nil {
		t.Fatal("启用后应返回工厂")
	}
	ctx := context.Background()
	first := f(ctx, outbound.GroupTarget(9))
	second := f(ctx, outbound.GroupTarget(9))
	if first == nil || second == nil {
		t.Fatal("工厂应每次都返回切分器")
	}
	if first == second {
		t.Fatal("切分器必须每次新建：它持有已发送前缀状态，复用会漏发")
	}
}
