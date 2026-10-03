package moderation

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func Test_F58_AntiSpamRateTriggersTempBan(t *testing.T) {
	clock := newFakeClock()
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	spam := NewAntiSpam(AntiSpamConfig{
		Window:      10 * time.Second,
		MaxMessages: 20,
		BanDuration: 60 * time.Second,
	}).WithClock(clock.Now)

	var mu sync.Mutex
	metrics := map[string]int{}
	eng := New(Options{
		Blacklist: bl,
		AntiSpam:  spam,
		Metrics: MetricsFunc(func(name string) {
			mu.Lock()
			metrics[name]++
			mu.Unlock()
		}),
	})
	ctx := context.Background()
	meta := Meta{UserID: 1, Addressed: true}

	for i := 0; i < 20; i++ {
		d, err := eng.Review(ctx, Message{Text: "msg-" + strconv.Itoa(i)}, meta)
		if err != nil {
			t.Fatalf("Review %d: %v", i, err)
		}
		if d.Blocked() {
			t.Fatalf("第 %d 条不应被拦截: %+v", i+1, d)
		}
	}

	d, err := eng.Review(ctx, Message{Text: "msg-20"}, meta)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !d.Blocked() || d.Rule != "antispam:rate" {
		t.Fatalf("第 21 条应触发限流: %+v", d)
	}
	if !strings.Contains(d.Reason, "短时间消息过多") {
		t.Fatalf("限流原因应明确: %q", d.Reason)
	}
	if _, ok := bl.Banned(BanUser, "1"); !ok {
		t.Fatal("触发防刷后应被临时封禁")
	}

	mu.Lock()
	rateCount := metrics[MetricRateLimited]
	banCount := metrics[MetricBanned]
	mu.Unlock()
	if rateCount == 0 || banCount == 0 {
		t.Fatalf("应记录限流与封禁指标: %+v", metrics)
	}

	clock.Advance(61 * time.Second)
	if _, ok := bl.Banned(BanUser, "1"); ok {
		t.Fatal("临时封禁时间到应自动解封")
	}
}

func Test_F58_AntiSpamDuplicate(t *testing.T) {
	clock := newFakeClock()
	spam := NewAntiSpam(AntiSpamConfig{
		Window:          10 * time.Second,
		MaxMessages:     100,
		BanDuration:     60 * time.Second,
		DuplicateRepeat: 3,
	}).WithClock(clock.Now)

	for i := 0; i < 2; i++ {
		if reason, _ := spam.Record("1", "复读机"); reason != SpamNone {
			t.Fatalf("第 %d 条不应命中: %v", i+1, reason)
		}
	}
	reason, text := spam.Record("1", "复读机")
	if reason != SpamDuplicate {
		t.Fatalf("第 3 条相同消息应命中重复: %v", reason)
	}
	if !strings.Contains(text, "重复") {
		t.Fatalf("重复原因应明确: %q", text)
	}
	// 不同文本重置计数。
	if reason, _ := spam.Record("1", "换一句"); reason != SpamNone {
		t.Fatalf("不同文本应重置: %v", reason)
	}
}

func Test_F58_AntiSpamGroupAndSweep(t *testing.T) {
	clock := newFakeClock()
	spam := NewAntiSpam(AntiSpamConfig{Window: time.Minute, MaxMessages: 5}).WithClock(clock.Now)
	for i := 0; i < 5; i++ {
		if reason, _ := spam.Record("g:9", "x-"+strconv.Itoa(i)); reason != SpamNone {
			t.Fatalf("第 %d 条不应命中: %v", i+1, reason)
		}
	}
	if reason, _ := spam.Record("g:9", "x-5"); reason != SpamRate {
		t.Fatalf("群维度第 6 条应命中: %v", reason)
	}
	if spam.Len() != 1 {
		t.Fatalf("应只跟踪 1 个 key，得到 %d", spam.Len())
	}
	clock.Advance(2 * time.Minute)
	if n := spam.Sweep(); n != 1 {
		t.Fatalf("空闲桶应被回收，得到 %d", n)
	}
	if spam.Len() != 0 {
		t.Fatalf("回收后应为空，得到 %d", spam.Len())
	}
}

func Test_F58_SpamReasonString(t *testing.T) {
	cases := map[SpamReason]string{
		SpamNone:      "none",
		SpamRate:      "rate",
		SpamDuplicate: "duplicate",
	}
	for reason, want := range cases {
		if got := reason.String(); got != want {
			t.Fatalf("%d.String()=%q，期望 %q", reason, got, want)
		}
	}
}
