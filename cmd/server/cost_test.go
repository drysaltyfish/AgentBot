package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// Test_F66_QuotaDenyHappensBeforeSpending 覆盖 F-66 的"超限动作默认 deny"：
//
// 拒绝必须发生在**调用之前**——拦在响应之后只是记账，钱已经花了。
// 断言的是行为：第二次调用根本没到 provider（桩被调用次数不增加）。
func Test_F66_QuotaDenyHappensBeforeSpending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cfg := config.Default()
	cfg.Cost.Enabled = ptr(true)
	cfg.Cost.Prices = map[string]config.CostPrice{"m": {InputPer1K: 10, OutputPer1K: 0}}
	cfg.Cost.Quotas = []config.CostQuota{{Scope: "session", Period: "total", Limit: 5, Action: "deny"}}

	tracker, err := buildCostTracker(cfg, testLogger(t), nil, nil)
	if err != nil {
		t.Fatalf("buildCostTracker: %v", err)
	}
	defer func() { _ = tracker.Close() }()

	stub := &countingLLM{resp: &llm.ChatResponse{Usage: llm.Usage{PromptTokens: 1000}}}
	obs := &observedLLM{next: stub, provider: "p", model: "m", cost: tracker}
	callCtx := cost.WithAttribution(ctx, "1:2:3", "42")

	if _, err := obs.Chat(callCtx, &llm.ChatRequest{}); err != nil {
		t.Fatalf("首次调用不该被拒: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("桩应被调用 1 次，实际 %d", stub.calls)
	}

	_, err = obs.Chat(callCtx, &llm.ChatRequest{})
	if err == nil {
		t.Fatal("超限后第二次调用必须被拒绝")
	}
	var qerr *cost.QuotaError
	if !errors.As(err, &qerr) {
		t.Fatalf("拒绝错误应可被 errors.As 取出 *cost.QuotaError: %v", err)
	}
	if qerr.Scope != cost.ScopeSession {
		t.Fatalf("拒绝应来自会话配额: %+v", qerr)
	}
	if stub.calls != 1 {
		t.Fatalf("被拒绝的调用不得触达 provider：桩调用次数=%d", stub.calls)
	}
}

// Test_F66_SessionQuotaIsPerSession 证明会话维度真的按会话隔离，
// 而不是"只要带个非空 key 就共用一份额度"。
func Test_F66_SessionQuotaIsPerSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Cost.Enabled = ptr(true)
	cfg.Cost.Prices = map[string]config.CostPrice{"m": {InputPer1K: 10, OutputPer1K: 0}}
	cfg.Cost.Quotas = []config.CostQuota{{Scope: "session", Period: "total", Limit: 5, Action: "deny"}}

	tracker, err := buildCostTracker(cfg, testLogger(t), nil, nil)
	if err != nil {
		t.Fatalf("buildCostTracker: %v", err)
	}
	defer func() { _ = tracker.Close() }()
	obs := &observedLLM{next: &countingLLM{resp: &llm.ChatResponse{Usage: llm.Usage{PromptTokens: 1000}}}, provider: "p", model: "m", cost: tracker}

	if _, err := obs.Chat(cost.WithAttribution(ctx, "a", "1"), &llm.ChatRequest{}); err != nil {
		t.Fatalf("会话 a 首次调用: %v", err)
	}
	if _, err := obs.Chat(cost.WithAttribution(ctx, "a", "1"), &llm.ChatRequest{}); err == nil {
		t.Fatal("会话 a 超限后应被拒")
	}
	if _, err := obs.Chat(cost.WithAttribution(ctx, "b", "2"), &llm.ChatRequest{}); err != nil {
		t.Fatalf("会话 b 有自己的额度，不该被 a 拖累: %v", err)
	}
}

// Test_F66_CostSnapshotPersistsThroughAdapter 覆盖 adapter 的编解码往返：
// 快照先经过 SQLite，再被读回，会话维度的值必须原样保留。
func Test_F66_CostSnapshotPersistsThroughAdapter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "cost.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	adapter := costStoreAdapter{st: st}
	if _, err := adapter.Load(); err != nil {
		t.Fatalf("空库 Load: %v", err)
	}
	snap := &cost.Snapshot{Buckets: []cost.Bucket{{Scope: cost.ScopeSession, Key: "1:2:3", Period: cost.PeriodTotal, Aggregate: cost.Aggregate{Calls: 7, Cost: 1.25}}}}
	if err := adapter.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := adapter.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || len(got.Buckets) != 1 || got.Buckets[0].Key != "1:2:3" || got.Buckets[0].Calls != 7 {
		t.Fatalf("快照往返失真: %+v", got)
	}
}

// Test_F66_BuildCostTrackerIsolatesSessions 是组合层的轻量回归：
// 配置里的一条会话配额必须真的建出会话维度，而不是被静默忽略。
func Test_F66_BuildCostTrackerIsolatesSessions(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Cost.Enabled = ptr(true)
	cfg.Cost.Quotas = []config.CostQuota{{Scope: "session", Period: "day", Limit: 1, Action: "warn"}}
	tr, err := buildCostTracker(cfg, testLogger(t), nil, nil)
	if err != nil {
		t.Fatalf("buildCostTracker: %v", err)
	}
	defer func() { _ = tr.Close() }()
	key := session.Key{SelfID: 1, GroupID: 2, UserID: 3}
	if _, err := tr.Record(cost.Call{Provider: "p", Model: "m", SessionKey: key.String(), PromptTokens: 1}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := tr.Session(key.String()); got.Calls != 1 {
		t.Fatalf("会话维度未被建立: %+v", got)
	}
	if strings.TrimSpace(key.String()) == "" {
		t.Fatal("会话键不应为空串（空键会让所有会话共用一个桶）")
	}
}

// countingLLM 是计数用的桩：用来证明"被拒绝的调用没有触达 provider"。
type countingLLM struct {
	resp  *llm.ChatResponse
	calls int
}

func (c *countingLLM) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	return c.resp, nil
}

func (c *countingLLM) ChatStream(context.Context, *llm.ChatRequest) (<-chan llm.Chunk, error) {
	c.calls++
	return nil, nil
}

var _ llm.LLM = (*countingLLM)(nil)
