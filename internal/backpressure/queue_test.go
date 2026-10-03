package backpressure

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// Test_F20_DropNewestCountsDrops 覆盖验收：队列满时丢弃新事件并计数，且不阻塞提交方。
func Test_F20_DropNewestCountsDrops(t *testing.T) {
	t.Parallel()
	var drops atomic.Int64
	// 不 Start：worker 不消费，队列稳定停在容量上，判据确定。
	q := New(func(int) {}, Options{Capacity: 1, Workers: 1, OnDrop: func(string) { drops.Add(1) }})

	if !q.Submit(1) {
		t.Fatalf("第一条应入队")
	}
	if q.Submit(2) {
		t.Fatalf("容量 1 时第二条应被丢弃")
	}
	if q.Submit(3) {
		t.Fatalf("第三条也应被丢弃")
	}
	if got := q.Stats().Dropped; got != 2 {
		t.Fatalf("丢弃计数: actual=%d expected=2", got)
	}
	if got := drops.Load(); got != 2 {
		t.Fatalf("OnDrop 回调次数: actual=%d expected=2", got)
	}
}

// Test_F20_DropOldestEvictsOldest 覆盖 DropOldest：挤掉最旧、保留最新。
func Test_F20_DropOldestEvictsOldest(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []int
	q := New(func(v int) {
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
	}, Options{Capacity: 1, Workers: 1, Policy: DropOldest})

	q.Submit(1)
	q.Submit(2)
	if st := q.Stats(); st.Dropped != 1 {
		t.Fatalf("应挤掉一条: dropped=%d", st.Dropped)
	}

	q.Start()
	if err := q.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("应只处理最新那条: actual=%v expected=[2]", got)
	}
}

// Test_F20_WorkerSurvivesPanic 覆盖边界：handler panic 不能杀死 worker。
func Test_F20_WorkerSurvivesPanic(t *testing.T) {
	t.Parallel()
	var handled atomic.Int64
	q := New(func(v int) {
		if v == 0 {
			panic("boom")
		}
		handled.Add(1)
	}, Options{Capacity: 4, Workers: 1, OnPanic: func(any) {}})

	q.Start()
	q.Submit(0)
	q.Submit(1)
	if err := q.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("panic 后 worker 应存活并处理下一条: actual=%d expected=1", got)
	}
}

// Test_F20_CloseDrainsAndIsIdempotent 覆盖关闭语义：排空、幂等、关闭后拒绝入队。
func Test_F20_CloseDrainsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	var n atomic.Int64
	q := New(func(int) { n.Add(1) }, Options{Capacity: 8, Workers: 2})

	q.Start()
	for i := 0; i < 5; i++ {
		if !q.Submit(i) {
			t.Fatalf("第 %d 条应入队", i)
		}
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := n.Load(); got != 5 {
		t.Fatalf("关闭应排空队列: actual=%d expected=5", got)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatalf("重复 Close 应幂等: %v", err)
	}
	if q.Submit(9) {
		t.Fatalf("关闭后不应再接受入队")
	}
}
