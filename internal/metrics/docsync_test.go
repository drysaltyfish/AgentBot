package metrics

import (
	"os"
	"strings"
	"testing"
)

// Test_F79_MetricNamesAreDocumented 保证代码里暴露的指标名都能在规范里 grep 到（F-79）。
//
// 规范是唯一出处：指标名改了却忘了改文档，抓取端会静默地看不到新名字。
func Test_F79_MetricNamesAreDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../FEATURES.md")
	if err != nil {
		t.Fatalf("read FEATURES.md: %v", err)
	}
	spec := string(raw)
	for _, name := range NewCatalog(CatalogOptions{}).MetricNames() {
		if !strings.Contains(spec, name) {
			t.Errorf("指标 %q 在 FEATURES.md 里找不到——文档与代码不同步", name)
		}
	}
}
