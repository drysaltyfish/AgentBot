package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// testCtx 造一个只带用户/群号的路由上下文。
func testCtx(userID, groupID int64) *router.Ctx {
	return &router.Ctx{Event: &event.Event{UserID: userID, GroupID: groupID}}
}

// Test_F24_RateLimitStateSwapsAtomically 覆盖热加载的核心机制：
// 规则注册一次、内部解引用当前参数，因此"先关后开"也必须立刻生效。
func Test_F24_RateLimitStateSwapsAtomically(t *testing.T) {
	t.Parallel()

	off := config.Default()
	off.RateLimit.Enabled = ptr(false)
	state := newRateLimitState(off.RateLimit, nil, nil)
	user, _ := state.Rules()

	c := testCtx(1, 2)
	// 关闭状态下恒真：限速不该在未启用时偷偷生效。
	for i := 0; i < 5; i++ {
		if !user(c) {
			t.Fatalf("未启用限速时第 %d 次调用不该被拒", i)
		}
	}

	// 运行时切到"每用户每分钟 1 次、突发 1"：同一用户第二次必须被拒。
	on := config.Default()
	on.RateLimit.Enabled = ptr(true)
	on.RateLimit.UserPerMinute = ptr(1)
	on.RateLimit.UserBurst = ptr(1)
	state.Store(on.RateLimit, nil, nil)

	if !user(c) {
		t.Fatal("新参数下第一次调用应放行")
	}
	if user(c) {
		t.Fatal("新参数下第二次调用必须被拒（突发只有 1）")
	}
	// 另一个用户有自己的桶，不受影响。
	if !user(testCtx(9, 2)) {
		t.Fatal("不同用户应各自计数")
	}
}

// Test_F24_RateLimitHotReloadFromConfigFile 是 F-24 的效果验收：
// 改配置文件后，新的限速参数应当从下一次请求开始生效，不必重启。
func Test_F24_RateLimitHotReloadFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfig := func(ratelimit string) {
		t.Helper()
		body := "transport:\n  mode: wsclient\n  url: ws://127.0.0.1:3001\nllm:\n  model: m\n" + ratelimit
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "\\n", "\n")), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	writeConfig("ratelimit:\n  enabled: false\n")

	lg := testLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	state := newRateLimitState(initial.RateLimit, nil, nil)
	w := watchRateLimit(ctx, path, state, nil, nil, lg)
	if w == nil {
		t.Fatal("watchRateLimit 应返回监听器")
	}
	defer w.Stop()

	user, _ := state.Rules()
	c := testCtx(42, 7)
	waitFor(t, "初始为关闭（恒真）", func() bool { return user(c) && user(c) })

	// 改成"开启 + 突发 1"：第二次调用应被拒。
	writeConfig("ratelimit:\n  enabled: true\n  user_per_minute: 1\n  user_burst: 1\n")
	waitFor(t, "热加载后限速生效", func() bool {
		// 桶会被上一次探测消耗，这里用"连续两次中至少一次被拒"来判定已切换。
		a := user(c)
		b := user(c)
		return !(a && b)
	})

	// 再放宽到突发 50：连续多次调用都应放行。
	writeConfig("ratelimit:\n  enabled: true\n  user_per_minute: 1\n  user_burst: 50\n")
	waitFor(t, "热加载后限速放宽", func() bool {
		for i := 0; i < 5; i++ {
			if !user(c) {
				return false
			}
		}
		return true
	})
}

// waitFor 轮询直到条件成立或超时。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}
