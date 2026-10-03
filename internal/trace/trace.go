// Package trace 实现 W3C Trace Context 的最小自包含子集（FEATURES.md F-72）。
//
// 为什么不用 OpenTelemetry：仓库的依赖政策（FEATURES §0.4）只引入白名单内的
// 第三方库，OTel SDK 与 OTLP 导出器不在其中。而追踪真正的协议面很小——把一次
// 调用的 trace_id/span_id 编码进 traceparent 头、随 context 透传、为下游派生
// 子 span。这些用标准库即可完整实现，因此本包是一个"够用且可替换"的子集：
//
//   - traceparent（version 00）的解析与格式化；
//   - TraceID/SpanID 的生成（crypto/rand）与合法性校验；
//   - SpanContext 在 context.Context 中的存取；
//   - 派生子 span（沿用 trace_id，换新 span_id）；
//   - 与采样率挂钩的确定性采样器。
//
// 本包不包含 OTel 的 span 导出（OTLP）、属性、事件、批处理与后台采样器。
// 接入完整 OTel 时，SpanContext 可与 remote span context 互相转换，属增量替换。
// 未配置导出端点时调用方只做上下文透传，单次操作是几个小结构体的复制，没有
// goroutine、没有 I/O、没有后台缓冲，满足 F-72"未配置即近零开销"的边界。
package trace

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

// Header 是 W3C trace context 的 HTTP 头名。
const Header = "traceparent"

// Version 是本包唯一生成与接受的 traceparent 版本。
const Version = "00"

// FlagSampled 是 trace-flags 的采样位（bit 0）。
const FlagSampled byte = 0x01

// DefaultSampleRatio 是未显式配置时的默认采样率（F-72：默认 1%）。
const DefaultSampleRatio = 0.01

// 编码长度常量。
const (
	traceIDBytes   = 16
	spanIDBytes    = 8
	traceparentLen = 55 // "00-" + 32 + "-" + 16 + "-" + 2
	versionEnd     = 2
	traceIDStart   = 3
	traceIDEnd     = 35
	spanIDStart    = 36
	spanIDEnd      = 52
	flagsStart     = 53
	flagsEnd       = 55
)

// 解析错误。调用方用 errors.Is 判定具体类别。
var (
	// ErrMalformed 表示 traceparent 长度或分隔符不符合 version 00 的布局。
	ErrMalformed = errors.New("trace: malformed traceparent")
	// ErrVersion 表示版本号无法处理（ff 或本包未实现的版本）。
	ErrVersion = errors.New("trace: unsupported traceparent version")
	// ErrInvalidTraceID 表示 trace-id 非 32 位十六进制或为全零。
	ErrInvalidTraceID = errors.New("trace: invalid trace id")
	// ErrInvalidSpanID 表示 span-id 非 16 位十六进制或为全零。
	ErrInvalidSpanID = errors.New("trace: invalid span id")
	// ErrInvalidFlags 表示 trace-flags 非法。
	ErrInvalidFlags = errors.New("trace: invalid trace flags")
	// ErrInvalidParent 表示试图用非法的父 SpanContext 派生子 span。
	ErrInvalidParent = errors.New("trace: invalid parent span context")
)

// TraceID 是 16 字节的 trace 标识；全零视为非法。
type TraceID [traceIDBytes]byte

// String 返回小写十六进制的 32 位表示。
func (t TraceID) String() string { return hex.EncodeToString(t[:]) }

// IsZero 报告 trace-id 是否为全零（W3C 要求其非法）。
func (t TraceID) IsZero() bool { return t == TraceID{} }

// SpanID 是 8 字节的 span 标识；全零视为非法。
type SpanID [spanIDBytes]byte

// String 返回小写十六进制的 16 位表示。
func (s SpanID) String() string { return hex.EncodeToString(s[:]) }

// IsZero 报告 span-id 是否为全零（W3C 要求其非法）。
func (s SpanID) IsZero() bool { return s == SpanID{} }

// SpanContext 是一次调用的远端标识：trace 标识、当前 span 标识与 trace-flags。
//
// 它是"线上表示"而非完整 Span：没有名称、属性、时间戳，因此可以自由复制，
// 适合放进 context.Context 贯穿一次事件处理。
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	Flags   byte
}

// IsValid 报告 trace-id 与 span-id 是否都非零。
func (s SpanContext) IsValid() bool { return !s.TraceID.IsZero() && !s.SpanID.IsZero() }

// Sampled 报告该上下文是否带采样标志。
func (s SpanContext) Sampled() bool { return s.Flags&FlagSampled != 0 }

// String 把 SpanContext 编码为 W3C traceparent；非法上下文返回空串。
func (s SpanContext) String() string {
	if !s.IsValid() {
		return ""
	}
	return Version + "-" + s.TraceID.String() + "-" + s.SpanID.String() + "-" + hex.EncodeToString([]byte{s.Flags})
}

// Parse 解析 W3C traceparent（version 00）。
//
// 只接受恰好 55 字符、分隔符正确、trace-id/span-id 非零且为十六进制的输入。
// 版本字段为 ff 或非 00 时返回 ErrVersion；本包按 F-72 的窄口径只实现 version 00。
func Parse(header string) (SpanContext, error) {
	if len(header) != traceparentLen {
		return SpanContext{}, fmt.Errorf("trace: parse %q: %w", header, ErrMalformed)
	}
	if header[versionEnd] != '-' || header[traceIDEnd] != '-' || header[spanIDEnd] != '-' {
		return SpanContext{}, fmt.Errorf("trace: parse %q: %w", header, ErrMalformed)
	}
	rawVersion := header[:versionEnd]
	if rawVersion == "ff" {
		return SpanContext{}, fmt.Errorf("trace: parse version %q: %w", rawVersion, ErrVersion)
	}
	if rawVersion != Version {
		return SpanContext{}, fmt.Errorf("trace: parse version %q: %w", rawVersion, ErrVersion)
	}
	traceID, ok := decodeTraceID(header[traceIDStart:traceIDEnd])
	if !ok || traceID.IsZero() {
		return SpanContext{}, fmt.Errorf("trace: parse %q: %w", header, ErrInvalidTraceID)
	}
	spanID, ok := decodeSpanID(header[spanIDStart:spanIDEnd])
	if !ok || spanID.IsZero() {
		return SpanContext{}, fmt.Errorf("trace: parse %q: %w", header, ErrInvalidSpanID)
	}
	flags, ok := decodeFlags(header[flagsStart:flagsEnd])
	if !ok {
		return SpanContext{}, fmt.Errorf("trace: parse %q: %w", header, ErrInvalidFlags)
	}
	return SpanContext{TraceID: traceID, SpanID: spanID, Flags: flags}, nil
}

// NewTraceID 生成一个非零的随机 trace-id。
func NewTraceID() (TraceID, error) {
	for {
		var id TraceID
		if _, err := rand.Read(id[:]); err != nil {
			return TraceID{}, fmt.Errorf("trace: generate trace id: %w", err)
		}
		if !id.IsZero() {
			return id, nil
		}
	}
}

// NewSpanID 生成一个非零的随机 span-id。
func NewSpanID() (SpanID, error) {
	for {
		var id SpanID
		if _, err := rand.Read(id[:]); err != nil {
			return SpanID{}, fmt.Errorf("trace: generate span id: %w", err)
		}
		if !id.IsZero() {
			return id, nil
		}
	}
}

// NewRoot 开启一条新 trace：随机 trace-id 与 span-id，采样位由 sampled 决定。
func NewRoot(sampled bool) (SpanContext, error) {
	traceID, err := NewTraceID()
	if err != nil {
		return SpanContext{}, fmt.Errorf("trace: new root: %w", err)
	}
	spanID, err := NewSpanID()
	if err != nil {
		return SpanContext{}, fmt.Errorf("trace: new root: %w", err)
	}
	var flags byte
	if sampled {
		flags |= FlagSampled
	}
	return SpanContext{TraceID: traceID, SpanID: spanID, Flags: flags}, nil
}

// Child 基于当前上下文派生子 span：沿用 trace-id 与 trace-flags，换一个随机 span-id。
func (s SpanContext) Child() (SpanContext, error) {
	if !s.IsValid() {
		return SpanContext{}, fmt.Errorf("trace: derive child: %w", ErrInvalidParent)
	}
	spanID, err := NewSpanID()
	if err != nil {
		return SpanContext{}, fmt.Errorf("trace: derive child: %w", err)
	}
	return SpanContext{TraceID: s.TraceID, SpanID: spanID, Flags: s.Flags}, nil
}

type spanContextKey struct{}

// WithSpanContext 把远端 span 上下文放进 ctx，供下游读取与透传。
func WithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, spanContextKey{}, sc)
}

// SpanContextFromContext 取出 ctx 中的 SpanContext；不存在或非法时返回 false。
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	if !ok || !sc.IsValid() {
		return SpanContext{}, false
	}
	return sc, true
}

// TraceIDFromContext 取出 ctx 中的 trace-id；不存在或非法时返回 false。
func TraceIDFromContext(ctx context.Context) (TraceID, bool) {
	sc, ok := SpanContextFromContext(ctx)
	if !ok {
		return TraceID{}, false
	}
	return sc.TraceID, true
}

// Sampler 是确定性的 trace 采样器：同样的 trace-id 永远得到同样的结论，
// 因此同一 trace 在链路各跳的采样决定一致（W3C trace-flags 的语义要求）。
type Sampler struct {
	ratio float64
}

// NewSampler 构造采样器，ratio 会被夹到 [0,1]；NaN 回落到默认采样率。
func NewSampler(ratio float64) Sampler {
	switch {
	case math.IsNaN(ratio):
		ratio = DefaultSampleRatio
	case ratio < 0:
		ratio = 0
	case ratio > 1:
		ratio = 1
	}
	return Sampler{ratio: ratio}
}

// Ratio 返回生效的采样率。
func (s Sampler) Ratio() float64 { return s.ratio }

// ShouldSample 决定一条 trace 是否采样：forced 为 true（如已知错误）时强制采样，
// 否则按 ratio 对 trace-id 做确定性阈值采样。
func (s Sampler) ShouldSample(id TraceID, forced bool) bool {
	if forced {
		return true
	}
	if s.ratio <= 0 {
		return false
	}
	if s.ratio >= 1 {
		return true
	}
	const span = uint64(1) << 63
	threshold := uint64(s.ratio * float64(span))
	return binary.BigEndian.Uint64(id[:8]) < threshold
}

// decodeTraceID 把 32 位十六进制串解码成 TraceID。
func decodeTraceID(s string) (TraceID, bool) {
	var id TraceID
	if len(s) != len(id)*2 {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return TraceID{}, false
	}
	return id, true
}

// decodeSpanID 把 16 位十六进制串解码成 SpanID。
func decodeSpanID(s string) (SpanID, bool) {
	var id SpanID
	if len(s) != len(id)*2 {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return SpanID{}, false
	}
	return id, true
}

// decodeFlags 把 2 位十六进制串解码成 trace-flags。
func decodeFlags(s string) (byte, bool) {
	var b [1]byte
	if len(s) != 2 {
		return 0, false
	}
	if _, err := hex.Decode(b[:], []byte(s)); err != nil {
		return 0, false
	}
	return b[0], true
}
