package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeSummarizer 是可计数的摘要注入点，用于证明摘要来自注入而非硬编码。
type fakeSummarizer struct {
	mu    sync.Mutex
	calls int
	fn    func(texts []string) (string, error)
}

// Summarize 实现 Summarizer。
func (f *fakeSummarizer) Summarize(_ context.Context, texts []string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn == nil {
		return strings.Join(texts, "|"), nil
	}
	return f.fn(texts)
}

// count 返回调用次数。
func (f *fakeSummarizer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeEmbedder 是可计数、可造错的向量注入点。
type fakeEmbedder struct {
	mu    sync.Mutex
	calls int
	err   error
	fn    func(text string) []float32
}

// Embed 实现 Embedder。
func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.fn != nil {
		return f.fn(text), nil
	}
	return []float32{float32(len(text))}, nil
}

// f52Chunks 生成 n 个带引用的文档块。
func f52Chunks(n int) []Chunk {
	out := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Chunk{
			Text: "文档块-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i%10)),
			Refs: []string{"ref-" + string(rune('0'+i%10))},
		})
	}
	return out
}

// Test_F52_BuildsMultiLevelTree 是规格验收：100 个文档块要建出 >= 2 层结构。
func Test_F52_BuildsMultiLevelTree(t *testing.T) {
	t.Parallel()
	tree := NewSummaryTree(TreeOptions{})
	rep := tree.Build(context.Background(), f52Chunks(100))
	if tree.Levels() < 2 {
		t.Fatalf("应建出 >=2 层，得到 %d（report=%+v）", tree.Levels(), rep)
	}
	if len(tree.Roots()) == 0 {
		t.Fatalf("应有根节点")
	}
	if rep.LastCompletedLevel < 1 {
		t.Fatalf("进度应记录已完成的层: %+v", rep)
	}
	if rep.TimedOut || rep.Canceled {
		t.Fatalf("正常构建不应超时/取消: %+v", rep)
	}
	// 结构一致性 + 不退化单链：内部节点至少有 MinCluster 个子节点。
	for level := 0; level < tree.Levels(); level++ {
		for _, n := range tree.NodesAtLevel(level) {
			if n.Summary != "" && len(n.Children) < DefaultTreeMinCluster {
				t.Fatalf("内部节点子节点过少（单链风险）: %+v", n)
			}
			for _, cid := range n.Children {
				child, ok := tree.Node(cid)
				if !ok {
					t.Fatalf("子节点 %d 不存在", cid)
				}
				if child.Parent != n.ID {
					t.Fatalf("子节点 %d 的父指针为 %d，期望 %d", cid, child.Parent, n.ID)
				}
			}
		}
	}
	if rep.Nodes != tree.Len() {
		t.Fatalf("报告节点数 %d 与树实际 %d 不一致", rep.Nodes, tree.Len())
	}
}

// Test_F52_SummaryFailureDegradesAndRecords 边界：摘要失败降级且失败簇有记录。
func Test_F52_SummaryFailureDegradesAndRecords(t *testing.T) {
	t.Parallel()
	sum := &fakeSummarizer{fn: func([]string) (string, error) {
		return "", errors.New("模型不可用")
	}}
	tree := NewSummaryTree(TreeOptions{Summarizer: sum, Branching: 4})
	rep := tree.Build(context.Background(), f52Chunks(8))
	if tree.Levels() < 2 {
		t.Fatalf("摘要失败也必须完成整树构建，得到 %d 层", tree.Levels())
	}
	if len(rep.FailedClusters) == 0 {
		t.Fatalf("失败簇必须有记录: %+v", rep)
	}
	if sum.count() == 0 || sum.count() != len(rep.FailedClusters) {
		t.Fatalf("每次失败簇应各调用一次注入摘要器: calls=%d failed=%d", sum.count(), len(rep.FailedClusters))
	}
	upper := tree.NodesAtLevel(1)
	if len(upper) == 0 || upper[0].Summary == "" {
		t.Fatalf("降级后仍应有拼接摘要: %+v", upper)
	}
}

// Test_F52_NilSummarizerSkipsLLMAndIsDeterministic 证明无注入时不调用任何模型，
// 且降级结果确定（两次构建逐节点相等）。
func Test_F52_NilSummarizerSkipsLLMAndIsDeterministic(t *testing.T) {
	t.Parallel()
	first := NewSummaryTree(TreeOptions{Branching: 2})
	second := NewSummaryTree(TreeOptions{Branching: 2})
	chunks := f52Chunks(4)
	rep := first.Build(context.Background(), chunks)
	second.Build(context.Background(), chunks)
	if len(rep.FailedClusters) != 0 {
		t.Fatalf("无注入摘要器不应算失败: %+v", rep)
	}
	for level := 0; level < first.Levels(); level++ {
		a, b := first.NodesAtLevel(level), second.NodesAtLevel(level)
		if len(a) != len(b) {
			t.Fatalf("第 %d 层节点数不确定: %d vs %d", level, len(a), len(b))
		}
		for i := range a {
			if a[i].Summary != b[i].Summary {
				t.Fatalf("降级摘要不确定: %q vs %q", a[i].Summary, b[i].Summary)
			}
		}
	}
}

// Test_F52_EmbeddingFailureSkipsCluster 边界：embedding 失败跳过该簇并继续。
func Test_F52_EmbeddingFailureSkipsCluster(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{err: errors.New("向量服务不可用")}
	tree := NewSummaryTree(TreeOptions{Embedder: emb, Branching: 4})
	rep := tree.Build(context.Background(), f52Chunks(8))
	if len(rep.SkippedClusters) != 2 {
		t.Fatalf("第 1 层有 2 个簇，都应被跳过: %+v", rep)
	}
	if len(rep.FailedClusters) != 0 {
		t.Fatalf("embedding 失败不应算摘要失败: %+v", rep)
	}
	if tree.Levels() != 1 {
		t.Fatalf("所有簇被跳过时应只剩叶子层，得到 %d", tree.Levels())
	}
	if tree.Len() != 8 {
		t.Fatalf("叶子必须保留: %d", tree.Len())
	}
}

// Test_F52_RetrievalMergesLevels 验证在每一层检索并合并。
func Test_F52_RetrievalMergesLevels(t *testing.T) {
	t.Parallel()
	sum := &fakeSummarizer{fn: func([]string) (string, error) { return "总览 摘要", nil }}
	tree := NewSummaryTree(TreeOptions{Summarizer: sum, Branching: 4})
	tree.Build(context.Background(), []Chunk{
		{Text: "细节 一"}, {Text: "细节 二"}, {Text: "细节 三"}, {Text: "细节 四"},
	})
	macro := tree.Search("总览", 5)
	if len(macro) == 0 || macro[0].Level != 1 {
		t.Fatalf("宏观问题应命中上层摘要: %+v", macro)
	}
	detail := tree.Search("细节", 5)
	if len(detail) != 4 {
		t.Fatalf("细节问题应命中全部叶子: %+v", detail)
	}
	for _, h := range detail {
		if h.Level != 0 {
			t.Fatalf("细节问题不应命中上层: %+v", h)
		}
	}
	if got := tree.Search("", 5); got != nil {
		t.Fatalf("空查询应返回 nil: %+v", got)
	}
}

// Test_F52_TimeoutRecordsProgressAndResume 验证总体超时记录与断点续建。
func Test_F52_TimeoutRecordsProgressAndResume(t *testing.T) {
	t.Parallel()
	tree := NewSummaryTree(TreeOptions{Branching: 4})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := tree.Build(ctx, f52Chunks(8))
	if !rep.Canceled && !rep.TimedOut {
		t.Fatalf("取消/超时必须被记录: %+v", rep)
	}
	if tree.Levels() != 1 {
		t.Fatalf("停止后应只提交叶子层: %d", tree.Levels())
	}
	resumed := tree.Resume(context.Background())
	if resumed.LastCompletedLevel < 1 {
		t.Fatalf("续建应推进进度: %+v", resumed)
	}
	if tree.Levels() < 2 {
		t.Fatalf("续建后应 >=2 层: %d", tree.Levels())
	}
}

// Test_F52_MaxNodesCap 验证树规模上限，防止无界增长。
func Test_F52_MaxNodesCap(t *testing.T) {
	t.Parallel()
	tree := NewSummaryTree(TreeOptions{Branching: 4, MaxNodes: 9})
	rep := tree.Build(context.Background(), f52Chunks(8))
	if !rep.CapReached {
		t.Fatalf("应触发节点上限: %+v", rep)
	}
	if tree.Len() != 8 {
		t.Fatalf("触发上限的整层不应提交，期望 8，得到 %d", tree.Len())
	}
}

// Test_F52_MaxNodeLenTruncates 验证单节点长度可配并生效。
func Test_F52_MaxNodeLenTruncates(t *testing.T) {
	t.Parallel()
	tree := NewSummaryTree(TreeOptions{Branching: 2, MaxNodeLen: 3})
	tree.Build(context.Background(), []Chunk{{Text: "aaaaa"}, {Text: "bbbbb"}})
	upper := tree.NodesAtLevel(1)
	if len(upper) != 1 {
		t.Fatalf("应有一个上层节点: %+v", upper)
	}
	if got := len([]rune(upper[0].Summary)); got != 3 {
		t.Fatalf("摘要应截断到 3 字符，得到 %d: %q", got, upper[0].Summary)
	}
}

// Test_F52_InjectedSummarizerCalled 证明摘要由注入的 Summarizer 生成。
func Test_F52_InjectedSummarizerCalled(t *testing.T) {
	t.Parallel()
	sum := &fakeSummarizer{}
	tree := NewSummaryTree(TreeOptions{Summarizer: sum, Branching: 4})
	tree.Build(context.Background(), f52Chunks(8))
	if sum.count() == 0 {
		t.Fatalf("注入的摘要器必须被调用")
	}
}

// Test_F52_SearchVector 验证向量检索路径。
func Test_F52_SearchVector(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{fn: func(text string) []float32 {
		if strings.Contains(text, "总览") {
			return []float32{1, 0}
		}
		return []float32{0, 1}
	}}
	tree := NewSummaryTree(TreeOptions{
		Embedder:   emb,
		Summarizer: &fakeSummarizer{fn: func([]string) (string, error) { return "总览", nil }},
		Branching:  2,
	})
	tree.Build(context.Background(), []Chunk{{Text: "细节一"}, {Text: "细节二"}})
	hits := tree.SearchVector([]float32{1, 0}, 5)
	if len(hits) == 0 || hits[0].Level != 1 {
		t.Fatalf("向量检索应命中摘要层: %+v", hits)
	}
	if got := tree.SearchVector(nil, 5); got != nil {
		t.Fatalf("空查询向量应返回 nil: %+v", got)
	}
}
