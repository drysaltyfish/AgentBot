package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// runExportMemories 把全部记忆导出为 JSONL（F-88）。
//
// 导出**跨作用域**，因此它是维护命令而不是会话内能力——
// 会话内只能看见自己的作用域。
func runExportMemories(cfg *config.Config, path string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.Open(ctx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: cfg.Store.BusyTimeoutOr(store.DefaultBusyTimeout),
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = st.Close() }()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = f.Close() }()

	n, err := st.ExportMemories(ctx, f)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	scopes, err := st.MemoryScopes(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "已导出 %d 条记忆（%d 个作用域）到 %s\n", n, len(scopes), path)
	return 0
}

// runStats 打印用量台账（F-85）。
//
// 这条命令存在的意义就是让"缓存命中率与花费"可以**查**，而不是只能翻日志。
func runStats(cfg *config.Config, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(ctx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: cfg.Store.BusyTimeoutOr(store.DefaultBusyTimeout),
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = st.Close() }()

	tot, err := st.UsageTotals(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "数据库: %s\n", st.Path())
	_, _ = fmt.Fprintf(stdout, "消息总数: %d\n", tot.MessageCount)
	_, _ = fmt.Fprintf(stdout, "请求总数: %d（工具调用 %d 次）\n", tot.Requests, tot.ToolCalls)
	_, _ = fmt.Fprintf(stdout, "token: 输入 %d / 输出 %d / 推理 %d\n",
		tot.InputTokens, tot.OutputTokens, tot.ReasoningTokens)
	_, _ = fmt.Fprintf(stdout, "前缀缓存: 命中 %d / 未命中 %d -> %.1f%%\n",
		tot.CacheHitTokens, tot.CacheMissTokens, tot.CacheHitRatio()*100)
	_, _ = fmt.Fprintf(stdout, "估计成本: $%.4f（价格版本 %q）\n", tot.EstimatedCostUSD, tot.PricingVersion)

	top, err := st.TopSessions(ctx, 5)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if len(top) > 0 {
		_, _ = fmt.Fprintln(stdout, "\n花费最高的会话:")
		for _, u := range top {
			_, _ = fmt.Fprintf(stdout, "  %-28s 请求 %4d  token 输入 %8d  命中 %5.1f%%  成本 $%.4f\n",
				u.SessionKey, u.Requests, u.InputTokens, u.CacheHitRatio()*100, u.EstimatedCostUSD)
		}
	}
	return 0
}

// runSelfTest 只做连通性验证：连接 → get_login_info → 发一条消息给指定用户 → 退出。
func runSelfTest(cfg *config.Config, target int64, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// 与 serve 用同一个构造点：自检必须验证**和线上完全一样**的鉴权配置，
	// 否则"自检通过"并不代表服务能连上。
	auth, aerr := buildTransportAuth(cfg)
	if aerr != nil {
		_, _ = fmt.Fprintf(stderr, "invalid transport auth config: %v\n", aerr)
		return 1
	}
	ws := transport.NewWSClient(cfg.Transport.URL, auth)
	defer func() { _ = ws.Close(context.Background()) }()

	if err := ws.Connect(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "connect %s failed: %v\n", cfg.Transport.URL, err)
		return 1
	}
	sender := outbound.NewSender(ws, outbound.New())
	go func() { _ = ws.Listen(ctx, func([]byte, transport.Caller) {}) }()

	resp, err := ws.Call(ctx, transport.Request{Action: "get_login_info"})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "get_login_info failed: %v\n", err)
		return 1
	}
	var info struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	_ = json.Unmarshal(resp.Data, &info)
	_, _ = fmt.Printf("connected: %s\nlogged in as: %d (%s)\n", cfg.Transport.URL, info.UserID, info.Nickname)

	id, err := sender.Send(ctx, outbound.PrivateTarget(target), event.Message{event.Text("AgentBot 自检：链路正常（可忽略）。")})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "send failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Printf("sent self-test message to %d, message_id=%s\n", target, id.String())
	return 0
}
