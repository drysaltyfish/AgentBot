package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/cost"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/metrics"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// foundation 是"模型 + 持久层 + 历史"这一层的装配结果。
//
// 抽出来的理由不是美观，而是**可测**：serve() 里这一段有四个错误出口、
// 两个 defer、一次一次性迁移，此前除了真正启动进程没有任何办法验证它。
// 现在它可以被单独构造并断言（见 foundation_test.go）。
type foundation struct {
	Model llm.LLM
	// Store 是唯一的内嵌 SQLite；打不开就启动失败，绝不降级为内存。
	Store *store.Store
	// History 是已按配置套好混合检索/摘要树的历史存储。
	History history.History
	// Budget 未启用时为 nil（不改动请求字节）。
	Budget *llm.Budget
	// Cost 未启用时为 nil。
	Cost *cost.Tracker
	// PromptWindow 是每轮真正回灌给模型的**条数**（一轮 ≈ user + assistant 两条）。
	// 与 History 的存储保留量分开：后者必须远大于前者，recall_history 才有东西可召回。
	PromptWindow int

	closers []func()
}

// Close 逆序释放本层持有的资源（成本台账 → 持久层）。
func (f *foundation) Close() {
	if f == nil {
		return
	}
	for i := len(f.closers) - 1; i >= 0; i-- {
		f.closers[i]()
	}
}

// buildFoundation 装配这一层并把失败原因直接写进日志，调用方只需 fail-fast。
//
// 顺序是有原因的：成本配额要重启后仍然有效，因此它的快照落 SQLite——
// 持久层必须先打开。
func buildFoundation(cfg *config.Config, lg *observe.Logger, catalog *metrics.Catalog) (*foundation, error) {
	lifecycle := lg.Component("lifecycle")
	f := &foundation{}

	model, err := buildLLM(cfg, lg)
	if err != nil {
		lifecycle.Error("cannot build llm", "error", err)
		return nil, err
	}
	f.Model = model

	// F-83：持久层。打不开就启动失败——不得静默降级为内存（那会悄悄丢数据）。
	// 打开与迁移给一个独立预算：卡住时要在启动阶段暴露，而不是拖到第一条消息。
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.Open(openCtx, store.Options{
		Path:        cfg.Store.Path,
		BusyTimeout: cfg.Store.BusyTimeoutOr(store.DefaultBusyTimeout),
	})
	cancelOpen()
	if err != nil {
		lifecycle.Error("cannot open the persistence store", "error", err, "path", cfg.Store.Path)
		return nil, err
	}
	f.Store = st
	f.closers = append(f.closers, func() {
		if cerr := st.Close(); cerr != nil {
			lifecycle.Warn("cannot close the persistence store", "error", cerr)
		}
	})
	lg.Component("store").Info("persistence store is ready",
		"path", st.Path(), "schema_version", store.SchemaVersion)

	// F-68：用装饰器收集模型调用的状态、延迟与 token，不改 internal/llm。
	// F-66：成本统计（默认关闭）。计量挂在 LLM 装饰器上，按 provider 真实 usage 记账。
	if cfg.Cost.EffectiveEnabled() {
		t, cerr := buildCostTracker(cfg, lg, catalog, costStoreAdapter{st: st})
		if cerr != nil {
			lifecycle.Error("cannot build cost tracker", "error", cerr)
			f.Close()
			return nil, cerr
		}
		f.Cost = t
		f.closers = append(f.closers, func() { _ = t.Close() })
	}

	// F-32：上下文预算（默认关闭——未配置 max_context 时预算为 nil，不改动请求字节）。
	// 预算与它的实测计数器一起拿回来：两者必须是同一个计数器，
	// 否则喂回去的实测值命不中预算的缓存（见 budget.go 的 budgetWiring）。
	bw := buildBudget(cfg, lg)
	f.Budget = bw.Budget
	if f.Budget != nil {
		lg.Component("llm").Info("context budget is enabled",
			"max_context", f.Budget.MaxContext,
			"reserve_output", f.Budget.ReserveOutput,
			"reserve_tools", f.Budget.ReserveTools,
			"counting", "provider usage first, heuristic fallback")
	}
	f.Model = &observedLLM{
		next: f.Model, cat: catalog, provider: providerName(cfg), model: cfg.LLM.Model,
		cost:    f.Cost,
		budget:  f.Budget,
		counter: bw.Counter,
		warn:    func(msg string) { lg.Component("cost").Warn(msg) },
	}

	// F-89：提示词快照是**环形保留**的，启动时裁一次即可保证有界。
	{
		pruneCtx, cancelPrune := context.WithTimeout(context.Background(), 15*time.Second)
		if n, perr := st.PrunePromptSnapshots(pruneCtx, promptSnapshotKeep); perr != nil {
			lg.Component("store").Warn("cannot prune prompt snapshots", "error", perr)
		} else if n > 0 {
			lg.Component("store").Info("pruned old prompt snapshots", "count", n, "keep_per_session", promptSnapshotKeep)
		}
		cancelPrune()
	}

	// 缓存优先（二）：历史裁剪交给存储层，且用高水位批量裁剪。
	// 若由装配层每轮裁剪，前缀会逐轮变化，前缀缓存永远无法命中。
	histItems := cfg.LLM.EffectiveHistoryTurns() * 2
	f.PromptWindow = histItems
	// **存储**保留量远大于呈现窗口：否则 recall_history 只能返回已经出现在
	// 提示词里的内容，等于摆设。两者分开是让那个工具真正有用的前提。
	retention := cfg.History.EffectiveRetention()
	if retention < histItems {
		retention = histItems
	}
	// F-84：历史落在持久层。JSONL 实现保留下来只用于导入与故障排查。
	sqliteHist := history.NewSQLite(st, retention).WithTrimmer(history.HighWater{
		Max: retention,
		Low: retention * 3 / 4,
	})
	f.History = wrapHistoryWithRetrieval(cfg, sqliteHist, lg)
	lg.Component("session").Info("conversation history is stored in the database",
		"retention", retention, "prompt_window", histItems)

	importLegacyHistory(cfg, sqliteHist, st, lg)
	return f, nil
}

// importLegacyHistory 把旧版 JSONL 历史一次性导入（幂等）。
//
// 只在库为空且配置了路径时触发，避免每次启动都白读一遍文件；
// 导入失败只告警不阻断——旧数据是加分项，不是启动前提。
func importLegacyHistory(cfg *config.Config, sqliteHist *history.SQLite, st *store.Store, lg *observe.Logger) {
	lifecycle := lg.Component("lifecycle")
	legacy := strings.TrimSpace(cfg.History.File)
	if legacy == "" {
		return
	}
	// 迁移是一次性的启动动作，给它独立预算，不占用请求 ctx。
	migCtx, cancelMig := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelMig()
	total, err := st.TotalMessageCount(migCtx)
	switch {
	case err != nil:
		lifecycle.Warn("cannot check message count; skipping legacy import", "error", err)
	case total > 0:
		lg.Component("session").Info("database already has messages; skipping legacy JSONL import",
			"messages", total, "legacy_path", legacy)
	default:
		imported, skipped, ierr := sqliteHist.ImportJSONL(migCtx, legacy)
		switch {
		case errors.Is(ierr, history.ErrImportSourceMissing):
			lg.Component("session").Info("no legacy history file to import", "path", legacy)
		case ierr != nil:
			lifecycle.Warn("legacy history import failed; starting with an empty history",
				"error", ierr, "path", legacy)
		default:
			lg.Component("session").Info("legacy JSONL history imported",
				"path", legacy, "imported", imported, "skipped", skipped)
		}
	}
}
