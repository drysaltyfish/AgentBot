package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openPendingStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t, Options{Path: filepath.Join(t.TempDir(), "pending.db")})
}

func Test_F86_PendingLifecycle(t *testing.T) {
	t.Parallel()
	s := openPendingStore(t)
	ctx := context.Background()

	if err := s.UpsertPending(ctx, Pending{
		ID: "a1", SessionKey: "k", Kind: "await", Payload: "等一条消息",
		ExpiresAt: nowMillis() + 60_000,
	}); err != nil {
		t.Fatalf("UpsertPending: %v", err)
	}

	list, err := s.ListPending(ctx, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("应有 1 条未结束记录: %v %v", list, err)
	}
	if list[0].Status != PendingStatusPending {
		t.Fatalf("新记录状态应为 pending: %+v", list[0])
	}

	if err := s.CompletePending(ctx, "a1", PendingStatusDone, "完成"); err != nil {
		t.Fatalf("CompletePending: %v", err)
	}
	if list, _ = s.ListPending(ctx, ""); len(list) != 0 {
		t.Fatalf("结束后不该再出现在未结束列表: %+v", list)
	}
	// 记录保留，状态可查（审计要求）。
	done, _ := s.ListPending(ctx, PendingStatusDone)
	if len(done) != 1 || done[0].Note != "完成" {
		t.Fatalf("结束状态与原因应可查: %+v", done)
	}
}

func Test_F86_UpsertIsIdempotent(t *testing.T) {
	t.Parallel()
	s := openPendingStore(t)
	ctx := context.Background()
	rec := Pending{ID: "same", SessionKey: "k", Kind: "await", ExpiresAt: nowMillis() + 1000}
	for i := 0; i < 3; i++ {
		if err := s.UpsertPending(ctx, rec); err != nil {
			t.Fatalf("UpsertPending: %v", err)
		}
	}
	if n, _ := s.CountPending(ctx); n != 1 {
		t.Fatalf("同 id 重复写入应只有一条: %d", n)
	}
}

func Test_F86_ExpireReturnsRecordsToNotify(t *testing.T) {
	t.Parallel()
	s := openPendingStore(t)
	ctx := context.Background()
	// 一条已过期、一条未过期。
	_ = s.UpsertPending(ctx, Pending{ID: "old", SessionKey: "k", Kind: "await",
		ExpiresAt: nowMillis() - 1000})
	_ = s.UpsertPending(ctx, Pending{ID: "new", SessionKey: "k", Kind: "await",
		ExpiresAt: nowMillis() + 60_000})

	expired, err := s.ExpirePending(ctx, 0)
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	// 必须**返回记录**而不只是条数：调用方要逐条回灌给原会话。
	if len(expired) != 1 || expired[0].ID != "old" {
		t.Fatalf("应返回过期的那条: %+v", expired)
	}
	if n, _ := s.CountPending(ctx); n != 1 {
		t.Fatalf("未过期的应保持 pending: %d", n)
	}
}

func Test_F86_PruneKeepsTableBounded(t *testing.T) {
	t.Parallel()
	s := openPendingStore(t)
	ctx := context.Background()
	// 一条早已结束、一条仍在进行。
	_ = s.UpsertPending(ctx, Pending{ID: "done", SessionKey: "k", Kind: "await",
		CreatedAt: nowMillis() - 30*24*3600*1000, ExpiresAt: nowMillis() - 30*24*3600*1000})
	_ = s.CompletePending(ctx, "done", PendingStatusDone, "老记录")
	_ = s.UpsertPending(ctx, Pending{ID: "live", SessionKey: "k", Kind: "await",
		ExpiresAt: nowMillis() + 60_000})

	n, err := s.PrunePending(ctx, nowMillis()-7*24*3600*1000)
	if err != nil {
		t.Fatalf("PrunePending: %v", err)
	}
	if n != 1 {
		t.Fatalf("应清掉 1 条老记录: %d", n)
	}
	// 进行中的记录**绝不能**被清掉。
	if c, _ := s.CountPending(ctx); c != 1 {
		t.Fatalf("进行中的记录不得被清理: %d", c)
	}
}

func Test_F86_RejectsBadInput(t *testing.T) {
	t.Parallel()
	s := openPendingStore(t)
	ctx := context.Background()
	if err := s.UpsertPending(ctx, Pending{SessionKey: "k"}); err == nil {
		t.Fatalf("空 id 必须报错")
	}
	if err := s.UpsertPending(ctx, Pending{ID: "x"}); err == nil {
		t.Fatalf("空会话键必须报错")
	}
	if err := s.CompletePending(ctx, "missing", "", ""); !errors.Is(err, ErrPendingNotFound) {
		t.Fatalf("结束不存在的记录应返回 ErrPendingNotFound: %v", err)
	}
}
