package event

import (
	"encoding/json"
	"testing"
)

// BenchmarkParseMessage 覆盖 F-77 的消息解析基准，载荷里带 CQ 字符串。
func BenchmarkParseMessage(b *testing.B) {
	raw := json.RawMessage(`[{"type":"text","data":{"text":"[CQ:at,qq=123] hello [CQ:image,file=x.jpg]"}},{"type":"image","data":{"file":"y.jpg"}}]`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ParseMessage(raw); err != nil {
			b.Fatalf("ParseMessage: %v", err)
		}
	}
}
