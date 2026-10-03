package cost

import (
	"context"
	"testing"
)

// Test_F66_AttributionTravelsThroughContext 钉住归属的传递方式：
// 会话键与用户标识经 ctx 到达 LLM 装饰器，而不是污染协议形状。
func Test_F66_AttributionTravelsThroughContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if a := AttributionFrom(ctx); !a.IsZero() {
		t.Fatalf("空 ctx 应得到零值归属: %+v", a)
	}
	ctx = WithAttribution(ctx, "1:2:3", "42")
	a := AttributionFrom(ctx)
	if a.SessionKey != "1:2:3" || a.UserID != "42" {
		t.Fatalf("归属未正确往返: %+v", a)
	}
	// 空归属不包装 ctx：省掉一次无意义的包装，也保证零值语义。
	if got := WithAttribution(ctx, "", ""); got != ctx {
		t.Fatal("空归属不该包装 ctx")
	}
}
