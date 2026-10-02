package testutil

import (
	"sync"
	"testing"
	"time"
)

func Test_F76_FakeClock_AfterFiresOnAdvance(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	c := NewFakeClock(start)

	ch := c.After(5 * time.Second)
	select {
	case <-ch:
		t.Fatalf("After fired before Advance: actual=fired expected=blocked")
	default:
	}

	c.Advance(3 * time.Second)
	select {
	case <-ch:
		t.Fatalf("After fired too early: actual=fired at +3s expected=blocked until +5s")
	default:
	}

	c.Advance(2 * time.Second)
	select {
	case got := <-ch:
		if !got.Equal(start.Add(5 * time.Second)) {
			t.Fatalf("fired at wrong time: actual=%v expected=%v", got, start.Add(5*time.Second))
		}
	case <-time.After(time.Second):
		t.Fatalf("After did not fire: actual=blocked expected=fired at +5s")
	}
	if n := c.PendingWaiters(); n != 0 {
		t.Fatalf("waiter leak: actual=%d expected=0", n)
	}
}

func Test_F76_FakeClock_NonPositiveDelayFiresImmediately(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(time.Unix(0, 0))
	select {
	case <-c.After(0):
	case <-time.After(time.Second):
		t.Fatalf("After(0) did not fire: actual=blocked expected=immediate")
	}
	if n := c.PendingWaiters(); n != 0 {
		t.Fatalf("waiter leak: actual=%d expected=0", n)
	}
}

func Test_F76_FakeClock_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(time.Unix(0, 0))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Now()
			ch := c.After(time.Second)
			select {
			case <-ch:
			default:
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			c.Advance(time.Millisecond)
		}
	}()
	wg.Wait()
}

func Test_F76_FakeClock_SetAfterFire(t *testing.T) {
	t.Parallel()
	c := NewFakeClock(time.Unix(100, 0))
	ch := c.After(10 * time.Second)
	c.Set(time.Unix(50, 0))
	select {
	case <-ch:
		t.Fatalf("After fired after clock was moved backwards: actual=fired expected=blocked")
	default:
	}
}
