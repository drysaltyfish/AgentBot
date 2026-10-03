package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Test_F66_CostSnapshotRoundTrip 覆盖 F-66 的持久化要求：
// 配额"若需严谨应持久化"，因此重启后必须能读回上一次的聚合。
func Test_F66_CostSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cost.db")

	first, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok, err := first.LoadCostSnapshot(ctx); err != nil || ok {
		t.Fatalf("未保存时应返回 (false,nil): ok=%v err=%v", ok, err)
	}
	if err := first.SaveCostSnapshot(ctx, []byte(`{"buckets":[]}`)); err != nil {
		t.Fatalf("SaveCostSnapshot: %v", err)
	}
	if err := first.SaveCostSnapshot(ctx, []byte(`{"buckets":[{"scope":"global"}]}`)); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()
	data, ok, err := second.LoadCostSnapshot(ctx)
	if err != nil || !ok {
		t.Fatalf("重开后读不到快照: ok=%v err=%v", ok, err)
	}
	if string(data) != `{"buckets":[{"scope":"global"}]}` {
		t.Fatalf("快照应为最后一次覆盖写的内容: %s", data)
	}
	if err := second.SaveCostSnapshot(ctx, nil); err == nil {
		t.Fatal("空快照必须被拒绝（静默写空等于把配额清零）")
	}
}
