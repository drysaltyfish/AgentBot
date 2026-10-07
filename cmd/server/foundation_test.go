package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/metrics"
)

// writeTextFile 写一个小文本文件（准备测试用的路径）。
func writeTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// foundationConfig 返回一份可以直接装配的配置：临时库 + echo 假模型，
// 不依赖网络、不碰真实数据目录。
func foundationConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.LLM.Provider = "echo"
	cfg.LLM.Model = "m"
	cfg.Store.Path = filepath.Join(t.TempDir(), "agentbot.db")
	return cfg
}

func newCatalog() *metrics.Catalog { return metrics.NewCatalog(metrics.CatalogOptions{}) }

// Test_BuildFoundationWiresTheDurableAndModelLayer 用一个测试覆盖这一整段的装配。
//
// 这段此前只有真正启动进程才能验证：四个错误出口、两个 defer、一次一次性迁移。
// 抽成 buildFoundation 之后，装配事实可以在这里逐条断言——
// 这正是评审说的"一个测试覆盖几十处装配"。
func Test_BuildFoundationWiresTheDurableAndModelLayer(t *testing.T) {
	t.Parallel()
	cfg := foundationConfig(t)

	f, err := buildFoundation(cfg, testLogger(t), newCatalog())
	if err != nil {
		t.Fatalf("buildFoundation: %v", err)
	}
	defer f.Close()

	if f.Model == nil {
		t.Fatal("Model 必须装配出来")
	}
	if _, ok := f.Model.(*observedLLM); !ok {
		t.Fatalf("模型必须被计量装饰器包住，实际 %T", f.Model)
	}
	if f.Store == nil {
		t.Fatal("Store 必须打开（打不开就该返回错误，而不是降级为内存）")
	}
	if got := f.Store.Path(); got != cfg.Store.Path {
		t.Fatalf("Store 路径: actual=%q expected=%q", got, cfg.Store.Path)
	}
	if f.History == nil {
		t.Fatal("History 必须装配出来")
	}
	// 默认不启用上下文预算与成本统计——两者都必须是 nil，而不是零值对象。
	if f.Budget != nil {
		t.Fatalf("未配置 max_context 时 Budget 必须为 nil（否则会改动请求字节）: %+v", f.Budget)
	}
	if f.Cost != nil {
		t.Fatalf("未启用 cost 时 Cost 必须为 nil: %+v", f.Cost)
	}
	if want := cfg.LLM.EffectiveHistoryTurns() * 2; f.PromptWindow != want {
		t.Fatalf("PromptWindow: actual=%d expected=%d", f.PromptWindow, want)
	}
}

// Test_BuildFoundationEnablesOptionalLayers 覆盖"打开开关之后真的有"。
func Test_BuildFoundationEnablesOptionalLayers(t *testing.T) {
	t.Parallel()
	cfg := foundationConfig(t)
	cfg.Cost.Enabled = ptr(true)
	cfg.LLM.MaxContext = ptr(4096)

	f, err := buildFoundation(cfg, testLogger(t), newCatalog())
	if err != nil {
		t.Fatalf("buildFoundation: %v", err)
	}
	defer f.Close()

	if f.Budget == nil {
		t.Fatal("配置了 max_context 之后 Budget 必须存在")
	}
	if f.Budget.MaxContext != 4096 {
		t.Fatalf("Budget.MaxContext: actual=%d expected=4096", f.Budget.MaxContext)
	}
	if f.Cost == nil {
		t.Fatal("启用 cost 之后 Cost 必须存在")
	}
}

// Test_BuildFoundationImportsLegacyHistoryOnce 覆盖嵌在装配里的那段一次性迁移。
//
// 它此前没法单独验证：只在空库时触发、失败只告警，而且藏在 serve() 中间。
func Test_BuildFoundationImportsLegacyHistoryOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := filepath.Join(dir, "history.jsonl")

	// 用真实的 File 实现写 JSONL，而不是我臆想的格式。
	ctx := context.Background()
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	src := history.NewFile(legacy, 100)
	for _, it := range []history.Item{
		{Kind: history.KindUser, Content: "旧消息一", At: at},
		{Kind: history.KindAssistant, Content: "旧消息二", At: at},
	} {
		if err := src.Append(ctx, "k1", it); err != nil {
			t.Fatalf("写旧 JSONL: %v", err)
		}
	}

	cfg := foundationConfig(t)
	cfg.Store.Path = filepath.Join(dir, "agentbot.db")
	cfg.History.File = legacy
	lg := testLogger(t)

	f, err := buildFoundation(cfg, lg, newCatalog())
	if err != nil {
		t.Fatalf("buildFoundation: %v", err)
	}
	after1, err := f.Store.TotalMessageCount(ctx)
	if err != nil {
		t.Fatalf("TotalMessageCount: %v", err)
	}
	f.Close()
	if after1 == 0 {
		t.Fatal("旧版 JSONL 历史没有被导入")
	}

	// 幂等：库已非空时再装配一次，不应重复写入。
	f2, err := buildFoundation(cfg, lg, newCatalog())
	if err != nil {
		t.Fatalf("第二次 buildFoundation: %v", err)
	}
	defer f2.Close()
	after2, err := f2.Store.TotalMessageCount(ctx)
	if err != nil {
		t.Fatalf("TotalMessageCount: %v", err)
	}
	if after2 != after1 {
		t.Fatalf("导入必须幂等: 第一次 %d 条，第二次 %d 条", after1, after2)
	}
}

// Test_BuildFoundationFailsOnUnopenableStore 打不开持久层必须报错，绝不降级为内存。
func Test_BuildFoundationFailsOnUnopenableStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 让父级是一个文件而不是目录：库路径不可能被创建。
	file := filepath.Join(dir, "not-a-dir")
	if err := writeTextFile(file, "x"); err != nil {
		t.Fatalf("准备路径: %v", err)
	}

	cfg := foundationConfig(t)
	cfg.Store.Path = filepath.Join(file, "agentbot.db")

	f, err := buildFoundation(cfg, testLogger(t), newCatalog())
	if err == nil {
		f.Close()
		t.Fatal("持久层打不开时必须返回错误（不得静默降级为内存）")
	}
}
