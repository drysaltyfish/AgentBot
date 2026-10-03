package moderation

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func Benchmark_F57_Review(b *testing.B) {
	eng := New(Options{Matcher: mustMatcher(b, "暴力", "**"), Sensitive: SensitiveMask})
	msg := Message{Text: "这是一段用于压测的普通文本，其中夹带暴力字样以触发扫描。"}
	meta := Meta{UserID: 1, Addressed: true}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Review(ctx, msg, meta); err != nil {
			b.Fatal(err)
		}
	}
}

func Benchmark_F58_Banned(b *testing.B) {
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := bl.Ban(BanUser, strconv.Itoa(i), "x", time.Hour); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bl.Banned(BanUser, "42")
	}
}

func Benchmark_F58_AntiSpamRecord(b *testing.B) {
	spam := NewAntiSpam(AntiSpamConfig{Window: time.Minute, MaxMessages: 1000})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		spam.Record("u:1", "hello")
	}
}
