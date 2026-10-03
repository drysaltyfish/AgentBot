package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/store"
)

func openSQLiteHistory(t *testing.T, max int) *SQLite {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "hist.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewSQLite(st, max)
}

func Test_F84_SQLiteRoundTrip(t *testing.T) {
	t.Parallel()
	h := openSQLiteHistory(t, 50)
	ctx := context.Background()
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	items := []Item{
		{Kind: KindUser, Content: "你好", At: at},
		{Kind: KindAssistant, Content: "", At: at, ToolCalls: []ToolCall{{ID: "c1", Name: "current_time", Arguments: "{}"}}},
		{Kind: KindToolResult, Content: "12:00", ToolCallID: "c1", At: at},
		{Kind: KindAssistant, Content: "现在是十二点", At: at},
	}
	for _, it := range items {
		if err := h.Append(ctx, "k", it); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := h.Messages(ctx, "k")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != len(items) {
		t.Fatalf("应取回 %d 条，实际 %d", len(items), len(got))
	}
	if got[1].Kind != KindAssistant || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].Name != "current_time" {
		t.Fatalf("工具调用未正确往返: %+v", got[1])
	}
	if got[2].Kind != KindToolResult || got[2].ToolCallID != "c1" {
		t.Fatalf("工具结果未正确往返: %+v", got[2])
	}
	if !got[0].At.Equal(at) {
		t.Fatalf("时间未保持: %v vs %v", got[0].At, at)
	}
}

func Test_F84_SQLiteTrimKeepsNewest(t *testing.T) {
	t.Parallel()
	h := openSQLiteHistory(t, 3).WithTrimmer(Window{N: 3})
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		if err := h.Append(ctx, "k", Item{Kind: KindUser, Content: string(rune('a' + i - 1))}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, _ := h.Messages(ctx, "k")
	if len(got) != 3 {
		t.Fatalf("应裁剪到 3 条，实际 %d", len(got))
	}
	if got[0].Content != "d" {
		t.Fatalf("应保留最新的 3 条: %v", contents(got))
	}
}

func contents(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Content)
	}
	return out
}

func Test_F84_SQLiteSearch(t *testing.T) {
	t.Parallel()
	h := openSQLiteHistory(t, 50)
	ctx := context.Background()
	for _, s := range []string{"我喜欢喝橙汁", "今天天气不错", "作业还没写完"} {
		if err := h.Append(ctx, "k", Item{Kind: KindUser, Content: s, At: time.Now()}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	hits, err := h.Search(ctx, "k", "橙汁", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Item.Content, "橙汁") {
		t.Fatalf("应命中 1 条: %+v", hits)
	}
	if hits[0].Snippet == "" {
		t.Fatalf("应带片段")
	}
	// 只搜本会话。
	if _, err := h.Search(ctx, "other", "橙汁", 10); err != nil {
		t.Fatalf("Search: %v", err)
	}
	none, _ := h.Search(ctx, "other", "橙汁", 10)
	if len(none) != 0 {
		t.Fatalf("不得跨会话: %+v", none)
	}
}

// Test_F83_ImportJSONLIsIdempotent 覆盖 F-83 的迁移验收。
func Test_F83_ImportJSONLIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "history.jsonl")

	// 先用 File 实现写出真实格式，保证导入读的是**真实**的 JSONL，而非我臆想的结构。
	f := NewFile(src, 100)
	ctx := context.Background()
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	want := []Item{
		{Kind: KindUser, Content: "第一条", At: at},
		{Kind: KindAssistant, Content: "第二条", At: at},
		{Kind: KindUser, Content: "第一条", At: at}, // 合法的重复内容
	}
	for _, it := range want {
		if err := f.Append(ctx, "k1", it); err != nil {
			t.Fatalf("写 JSONL: %v", err)
		}
	}
	if err := f.Append(ctx, "k2", Item{Kind: KindUser, Content: "别的会话", At: at}); err != nil {
		t.Fatalf("写 JSONL: %v", err)
	}

	h := openSQLiteHistory(t, 100)
	imported, skipped, err := h.ImportJSONL(ctx, src)
	if err != nil {
		t.Fatalf("ImportJSONL: %v", err)
	}
	if imported != 4 || skipped != 0 {
		t.Fatalf("首次导入应 4 条全进: imported=%d skipped=%d", imported, skipped)
	}

	// 再导一次：必须全部跳过，结果不变。
	imported2, skipped2, err := h.ImportJSONL(ctx, src)
	if err != nil {
		t.Fatalf("ImportJSONL 第二次: %v", err)
	}
	if imported2 != 0 || skipped2 != 4 {
		t.Fatalf("重复导入应全部跳过: imported=%d skipped=%d", imported2, skipped2)
	}

	got, _ := h.Messages(ctx, "k1")
	if len(got) != 3 {
		t.Fatalf("k1 应有 3 条（含合法重复）: %v", contents(got))
	}
	if got[0].Content != "第一条" || got[2].Content != "第一条" {
		t.Fatalf("按位置分配序号，合法的重复内容不得被吃掉: %v", contents(got))
	}
	got2, _ := h.Messages(ctx, "k2")
	if len(got2) != 1 || got2[0].Content != "别的会话" {
		t.Fatalf("k2 应有 1 条: %v", contents(got2))
	}
}

func Test_F83_ImportJSONLMissingFile(t *testing.T) {
	t.Parallel()
	h := openSQLiteHistory(t, 10)
	_, _, err := h.ImportJSONL(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"))
	if !errors.Is(err, ErrImportSourceMissing) {
		t.Fatalf("缺失源文件应返回 ErrImportSourceMissing: %v", err)
	}
}

func Test_F83_ImportJSONLRejectsBadLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatalf("准备坏文件: %v", err)
	}
	h := openSQLiteHistory(t, 10)
	if _, _, err := h.ImportJSONL(context.Background(), bad); err == nil {
		t.Fatalf("坏行必须报错，不能静默跳过")
	}
}
