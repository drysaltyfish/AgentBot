package main

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/memory"
	"github.com/drysaltyfish/agentbot/internal/metrics"
)

// stubJudge 返回预设结果，用于驱动计数装饰器。
type stubJudge struct {
	verdict bool
	err     error
}

func (s stubJudge) SameFact(context.Context, string, string) (bool, error) {
	return s.verdict, s.err
}

var _ memory.Judge = stubJudge{}

// Test_CountingJudgeCountsEveryOutcome 把判官的四种结果分别计一次。
//
// 这层计数是"要不要改用 provider 原生结构化输出（F-31）"的唯一依据：
// 判官解析失败会**静默退化**成确定性相似度，没有别的地方记录它。
func Test_CountingJudgeCountsEveryOutcome(t *testing.T) {
	t.Parallel()
	outcomes := []string{"same", "different", "unparsed", "error"}
	cases := []struct {
		name    string
		judge   stubJudge
		outcome string
	}{
		{"same", stubJudge{verdict: true}, "same"},
		{"different", stubJudge{verdict: false}, "different"},
		{"unparsed", stubJudge{err: memory.ErrJudgeUnparsed}, "unparsed"},
		{"error", stubJudge{err: errors.New("judge call: boom")}, "error"},
	}
	for _, tc := range cases {
		cat := metrics.NewCatalog(metrics.CatalogOptions{})
		j := countingJudge{next: tc.judge, cat: cat}
		_, _ = j.SameFact(context.Background(), "a", "b")

		if got := judgeOutcomeValue(t, cat, tc.outcome); got != 1 {
			t.Fatalf("%s: outcome=%q 应记 1 次，实际 %v", tc.name, tc.outcome, got)
		}
		for _, other := range outcomes {
			if other == tc.outcome {
				continue
			}
			if got := judgeOutcomeValue(t, cat, other); got != 0 {
				t.Fatalf("%s: 不该误记 outcome=%q（实际 %v）", tc.name, other, got)
			}
		}
	}
}

// Test_CountingJudgeForwardsTheVerdict 装饰器不得改变判定结果或错误。
func Test_CountingJudgeForwardsTheVerdict(t *testing.T) {
	t.Parallel()
	j := countingJudge{next: stubJudge{verdict: true}, cat: metrics.NewCatalog(metrics.CatalogOptions{})}
	ok, err := j.SameFact(context.Background(), "a", "b")
	if err != nil || !ok {
		t.Fatalf("判定结果被装饰器改变了: ok=%v err=%v", ok, err)
	}

	sentinel := memory.ErrJudgeUnparsed
	j2 := countingJudge{next: stubJudge{err: sentinel}, cat: metrics.NewCatalog(metrics.CatalogOptions{})}
	_, err = j2.SameFact(context.Background(), "a", "b")
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误必须原样透传（调用方靠 errors.Is 决定是否退回启发式）: %v", err)
	}
}

// Test_CountingJudgeToleratesMissingCatalog 没有指标目录时不能 panic。
func Test_CountingJudgeToleratesMissingCatalog(t *testing.T) {
	t.Parallel()
	j := countingJudge{next: stubJudge{verdict: true}}
	if _, err := j.SameFact(context.Background(), "a", "b"); err != nil {
		t.Fatalf("cat 为 nil 时应照常工作: %v", err)
	}
}

// judgeOutcomeValue 读某个 outcome 的判官计数。
func judgeOutcomeValue(t *testing.T, cat *metrics.Catalog, outcome string) float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := cat.Registry.WritePrometheus(&buf); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	want := `outcome="` + outcome + `"`
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "memory_judge_verdicts_total{") || !strings.Contains(line, want) {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("解析指标值失败: %q", line)
		}
		return v
	}
	return 0
}
