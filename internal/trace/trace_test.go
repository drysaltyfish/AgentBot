package trace

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

// w3cSample 是 W3C Trace Context 规范里的示例 traceparent。
const w3cSample = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func Test_F72_RoundTrip(t *testing.T) {
	sc, err := Parse(w3cSample)
	if err != nil {
		t.Fatalf("Parse(%q) 失败: %v", w3cSample, err)
	}
	if got := sc.TraceID.String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q，期望 W3C 示例值", got)
	}
	if got := sc.SpanID.String(); got != "00f067aa0ba902b7" {
		t.Fatalf("span id = %q，期望 W3C 示例值", got)
	}
	if !sc.Sampled() {
		t.Fatalf("flags=0x%02x，应带采样位", sc.Flags)
	}
	if got := sc.String(); got != w3cSample {
		t.Fatalf("String() = %q，期望逐字节等于输入", got)
	}

	// 再次解析格式化结果必须等价，保证 String/Parse 互逆。
	again, err := Parse(sc.String())
	if err != nil {
		t.Fatalf("重新 Parse(String()) 失败: %v", err)
	}
	if again != sc {
		t.Fatalf("往返后不一致: %+v != %+v", again, sc)
	}

	// 大写十六进制可以解析，但输出一律小写（规范化）。
	upper := "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01"
	up, err := Parse(upper)
	if err != nil {
		t.Fatalf("大写输入应可解析: %v", err)
	}
	if up.String() != w3cSample {
		t.Fatalf("大写输入应规范化为小写，得到 %q", up.String())
	}
}

func Test_F72_Parse_Invalid(t *testing.T) {
	const (
		validTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
		validSpan  = "00f067aa0ba902b7"
	)
	cases := []struct {
		name   string
		header string
		want   error
	}{
		{"empty", "", ErrMalformed},
		{"too short", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7", ErrMalformed},
		{"too long", w3cSample + "-extra", ErrMalformed},
		{"no separators", "004bf92f3577b34da6a3ce929d0e0e473600f067aa0ba902b701", ErrMalformed},
		{"bad separator", "00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", ErrMalformed},
		{"non hex trace", "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01", ErrInvalidTraceID},
		{"zero trace", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", ErrInvalidTraceID},
		{"bad middle separator", "00-" + validTrace + "_" + validSpan + "-01", ErrMalformed},
		{"bad flags separator", "00-" + validTrace + "-" + validSpan + "_01", ErrMalformed},
		{"non hex span", "00-" + validTrace + "-zzzzzzzzzzzzzzzz-01", ErrInvalidSpanID},
		{"zero span", "00-" + validTrace + "-0000000000000000-01", ErrInvalidSpanID},
		{"non hex flags", "00-" + validTrace + "-" + validSpan + "-zz", ErrInvalidFlags},
		{"version ff", "ff-" + validTrace + "-" + validSpan + "-01", ErrVersion},
		{"version 01", "01-" + validTrace + "-" + validSpan + "-01", ErrVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.header)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse(%q) 错误 = %v，期望 %v", tc.header, err, tc.want)
			}
		})
	}
}

func Test_F72_ContextPropagation(t *testing.T) {
	base := context.Background()
	if _, ok := SpanContextFromContext(base); ok {
		t.Fatalf("空 context 不应带 span 上下文")
	}
	if _, ok := TraceIDFromContext(base); ok {
		t.Fatalf("空 context 不应带 trace id")
	}

	parent, err := Parse(w3cSample)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	ctx := WithSpanContext(base, parent)
	got, ok := SpanContextFromContext(ctx)
	if !ok || got != parent {
		t.Fatalf("从 context 取回 = %+v, ok=%v，期望 %+v", got, ok, parent)
	}
	tid, ok := TraceIDFromContext(ctx)
	if !ok || tid != parent.TraceID {
		t.Fatalf("TraceIDFromContext = %v, ok=%v，期望 %v", tid, ok, parent.TraceID)
	}

	// 原 context 不受影响。
	if _, ok := SpanContextFromContext(base); ok {
		t.Fatalf("派生 context 不应污染原 context")
	}

	child, err := got.Child()
	if err != nil {
		t.Fatalf("Child 失败: %v", err)
	}
	childCtx := WithSpanContext(ctx, child)
	gotChild, _ := SpanContextFromContext(childCtx)
	if gotChild.TraceID != parent.TraceID {
		t.Fatalf("子 span 应沿用 trace id")
	}
	if gotChild.SpanID == parent.SpanID {
		t.Fatalf("子 span 的 span id 不应等于父 span")
	}
	if gotChild.Flags != parent.Flags {
		t.Fatalf("子 span 应沿用 trace flags")
	}
}

func Test_F72_ID_Uniqueness(t *testing.T) {
	const n = 1000
	traces := make(map[TraceID]struct{}, n)
	spans := make(map[SpanID]struct{}, n)
	for i := 0; i < n; i++ {
		tid, err := NewTraceID()
		if err != nil {
			t.Fatalf("NewTraceID 失败: %v", err)
		}
		if tid.IsZero() {
			t.Fatalf("生成的 trace id 不得为全零")
		}
		if _, dup := traces[tid]; dup {
			t.Fatalf("trace id 重复: %s", tid)
		}
		traces[tid] = struct{}{}

		sid, err := NewSpanID()
		if err != nil {
			t.Fatalf("NewSpanID 失败: %v", err)
		}
		if sid.IsZero() {
			t.Fatalf("生成的 span id 不得为全零")
		}
		if _, dup := spans[sid]; dup {
			t.Fatalf("span id 重复: %s", sid)
		}
		spans[sid] = struct{}{}
	}
}

func Test_F72_NewRoot_And_Child(t *testing.T) {
	root, err := NewRoot(true)
	if err != nil {
		t.Fatalf("NewRoot 失败: %v", err)
	}
	if !root.IsValid() || !root.Sampled() {
		t.Fatalf("NewRoot(sampled=true) = %+v，应为有效且已采样", root)
	}
	if _, err := Parse(root.String()); err != nil {
		t.Fatalf("NewRoot 的 String() 应可解析: %v", err)
	}

	child, err := root.Child()
	if err != nil {
		t.Fatalf("Child 失败: %v", err)
	}
	if child.TraceID != root.TraceID || child.SpanID == root.SpanID || !child.Sampled() {
		t.Fatalf("子 span 派生不正确: %+v from %+v", child, root)
	}

	if _, err := (SpanContext{}).Child(); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("非法父上下文的 Child 错误 = %v，期望 ErrInvalidParent", err)
	}
}

func Test_F72_Sampler(t *testing.T) {
	if got := NewSampler(math.NaN()).Ratio(); got != DefaultSampleRatio {
		t.Fatalf("NaN 采样率 = %v，期望默认 %v", got, DefaultSampleRatio)
	}
	if got := NewSampler(-1).Ratio(); got != 0 {
		t.Fatalf("负采样率应夹到 0，得到 %v", got)
	}
	if got := NewSampler(2).Ratio(); got != 1 {
		t.Fatalf("超界采样率应夹到 1，得到 %v", got)
	}

	never := NewSampler(0)
	always := NewSampler(1)
	id, err := NewTraceID()
	if err != nil {
		t.Fatalf("NewTraceID 失败: %v", err)
	}
	if never.ShouldSample(id, false) {
		t.Fatalf("采样率 0 不应采样")
	}
	if !always.ShouldSample(id, false) {
		t.Fatalf("采样率 1 应采样")
	}
	if !never.ShouldSample(id, true) {
		t.Fatalf("强制采样（错误）应忽略采样率")
	}

	// 同一 trace id 的判定必须稳定（跨跳一致）。
	half := NewSampler(0.5)
	first := half.ShouldSample(id, false)
	for i := 0; i < 8; i++ {
		if half.ShouldSample(id, false) != first {
			t.Fatalf("同一 trace id 的采样判定不稳定")
		}
	}
	// 0.5 采样率下 128 个随机 id 不应全落同一侧，否则阈值逻辑明显失效。
	sampled, skipped := 0, 0
	for i := 0; i < 128; i++ {
		other, gerr := NewTraceID()
		if gerr != nil {
			t.Fatalf("NewTraceID 失败: %v", gerr)
		}
		if half.ShouldSample(other, false) {
			sampled++
		} else {
			skipped++
		}
	}
	if sampled == 0 || skipped == 0 {
		t.Fatalf("0.5 采样率下分布异常: sampled=%d skipped=%d", sampled, skipped)
	}
}

func Test_F72_String_Invalid(t *testing.T) {
	if got := (SpanContext{}).String(); got != "" {
		t.Fatalf("非法 SpanContext 的 String() = %q，期望空串", got)
	}
	if strings.TrimSpace((TraceID{}).String()) == "" {
		t.Fatalf("全零 TraceID 的 String() 应为 32 个 0")
	}
}
