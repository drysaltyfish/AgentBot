package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/retry"
)

// deadConnDriver 模拟"连接已死"：Listen 立即失败，且**只有重新 Connect 才可能恢复**。
type deadConnDriver struct {
	connects atomic.Int64
	listens  atomic.Int64
}

func (d *deadConnDriver) Connect(context.Context) error {
	d.connects.Add(1)
	return nil
}

func (d *deadConnDriver) Listen(context.Context, Sink) error {
	d.listens.Add(1)
	// 连接已死：在**同一条**连接上重试读，结果不会变。
	return errors.New("read frame: EOF")
}

// Test_F04_RetryDriverListenDoesNotReconnect 把一处**陷阱**写成断言。
//
// `RetryDriver.Listen` 会重试 `next.Listen`，但**从不重新 Connect**：
// 连接断开后，重试的还是在同一条死连接上读，只能把同一个错误重复
// MaxAttempts 次然后放弃。而 F-04 要的是"连接失败的重试策略"——
// 那必须把连接**重新建起来**，光重试读是做不到的。
//
// 这条断言的是**危险行为本身**（重试期间 Connect 一次都没被调用）。
// 真正能恢复的是组合根的 `runWSSession`：Connect → Listen → `Disconnect` → 再 Connect。
func Test_F04_RetryDriverListenDoesNotReconnect(t *testing.T) {
	t.Parallel()
	d := &deadConnDriver{}
	rd := NewRetryDriver(d, retry.Policy{
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})

	if err := rd.Listen(context.Background(), func([]byte, Caller) {}); err == nil {
		t.Fatal("连接已死时 Listen 应当返回错误")
	}
	if got := d.listens.Load(); got != 3 {
		t.Fatalf("Listen 重试次数: actual=%d expected=3（MaxAttempts）", got)
	}
	if got := d.connects.Load(); got != 0 {
		t.Fatalf("本测试断言的是「重试不会重新连接」这个事实，但 Connect 被调用了 %d 次——"+
			"若这里变成非 0，说明 RetryDriver 已经学会重连，请更新它的注释与 HANDOFF 第 17 条", got)
	}
}

// alwaysFailConnectDriver 的 Connect 永远失败。
type alwaysFailConnectDriver struct {
	connects atomic.Int64
}

func (d *alwaysFailConnectDriver) Connect(context.Context) error {
	d.connects.Add(1)
	return errors.New("dial: connection refused")
}

func (d *alwaysFailConnectDriver) Listen(context.Context, Sink) error { return nil }

// Test_F04_RetryDriverWithDefaultPolicyGivesUpPermanently 钉住第二个陷阱：**尝试次数有上限**。
//
// F-04 的措辞是"默认 1s 起、最长 30s、带 jitter"——描述的是**退避上限**，
// 隐含"一直重试、退避封顶"。而 `retry.Default()` 带 `MaxAttempts: 3`，
// 于是 3 次之后**永久放弃**：平台晚几分钟回来，进程就再也连不上了。
//
// 这正是第 23 轮修掉的那个缺陷形态（进程还在、`/readyz` 还报 ready、机器人永远收不到消息）。
// 组合根的 `runWSSession` 不复用 `retry.Do` 的次数上限，只复用它的 `Delay`，因此没有这个问题。
func Test_F04_RetryDriverWithDefaultPolicyGivesUpPermanently(t *testing.T) {
	t.Parallel()
	p := retry.Default()
	p.Sleep = func(context.Context, time.Duration) error { return nil } // 测试里不真等

	d := &alwaysFailConnectDriver{}
	rd := NewRetryDriver(d, p)

	err := rd.Connect(context.Background())
	if err == nil {
		t.Fatal("一直连不上时应当返回错误")
	}
	if !errors.Is(err, retry.ErrAttemptsExhausted) {
		t.Fatalf("错误应表明尝试次数用尽: actual=%v", err)
	}
	if got := d.connects.Load(); got != int64(p.MaxAttempts) {
		t.Fatalf("Connect 尝试次数: actual=%d expected=%d（retry.Default 的上限）", got, p.MaxAttempts)
	}
}
