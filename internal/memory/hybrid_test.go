package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/vector"
)

// stubKeyword 是可注入的关键词一路，用于构造命中与故障。
type stubKeyword struct {
	hits []KeywordHit
	err  error
}

func (s stubKeyword) Search(string, int) ([]KeywordHit, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}

// stubVector 是可注入的向量一路。
type stubVector struct {
	hits []VectorHit
	err  error
}

func (s stubVector) Search([]float32, int) ([]VectorHit, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.hits, nil
}

// reverseReranker 反转输入顺序，用于验证重排确实被调用。
type reverseReranker struct{}

func (reverseReranker) Rerank(_ context.Context, _ string, hits []HybridHit) ([]HybridHit, error) {
	out := make([]HybridHit, len(hits))
	for i := range hits {
		out[len(hits)-1-i] = hits[i]
	}
	return out, nil
}

// f51Vec 返回 64 维全 ±1 向量；正负号不同即彼此汉明距离为 64。
func f51Vec(sign float32) []float32 {
	v := make([]float32, 64)
	for i := range v {
		v[i] = sign
	}
	return v
}

// Test_F51_TokenizeHandlesChineseAndAscii 验证中文单字/二字组与 ASCII 词混合分词。
func Test_F51_TokenizeHandlesChineseAndAscii(t *testing.T) {
	t.Parallel()
	got := tokenize("k8s 集群 v2")
	want := map[string]bool{"k8s": false, "v2": false, "集": false, "群": false, "集群": false}
	for _, tok := range got {
		if _, ok := want[tok]; ok {
			want[tok] = true
		}
	}
	for tok, seen := range want {
		if !seen {
			t.Fatalf("分词缺少 %q，得到 %v", tok, got)
		}
	}
	if uniqueTokens([]string{"a", "a", "b"})[1] != "b" {
		t.Fatalf("uniqueTokens 应保序去重")
	}
}

// Test_F51_BM25ChineseRanking 验证中文 BM25 命中专有名词且分数单调不增。
func Test_F51_BM25ChineseRanking(t *testing.T) {
	t.Parallel()
	idx := NewBM25Index()
	idx.Add(1, "北京烤鸭店的招牌菜")
	idx.Add(2, "北京今天的天气")
	idx.Add(3, "南京的雨很大")
	hits, err := idx.Search("北京烤鸭", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != 1 {
		t.Fatalf("专有名词应命中 id=1: %+v", hits)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Score < hits[i].Score {
			t.Fatalf("分数必须单调不增: %+v", hits)
		}
	}
	if empty, _ := idx.Search("北京烤鸭", 0); empty != nil {
		t.Fatalf("k<=0 应返回 nil")
	}
	if empty, _ := idx.Search("", 10); empty != nil {
		t.Fatalf("空查询应返回 nil")
	}
}

// Test_F51_BM25AddUpdateRemove 验证倒排索引在覆盖与删除后仍一致。
func Test_F51_BM25AddUpdateRemove(t *testing.T) {
	t.Parallel()
	idx := NewBM25Index()
	idx.Add(1, "北京烤鸭")
	if idx.Len() != 1 {
		t.Fatalf("Len = %d，期望 1", idx.Len())
	}
	idx.Add(1, "南京板鸭")
	if stale, _ := idx.Search("烤", 10); len(stale) != 0 {
		t.Fatalf("覆盖后旧文档独有的 token 必须消失: %+v", stale)
	}
	hits, _ := idx.Search("板", 10)
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("覆盖后应命中断词: %+v", hits)
	}
	if !idx.Remove(1) || idx.Remove(1) {
		t.Fatalf("Remove 首次为 true、重复为 false")
	}
	if idx.Len() != 0 {
		t.Fatalf("删除后 Len 应为 0")
	}
	if hits, _ := idx.Search("南京", 10); len(hits) != 0 {
		t.Fatalf("删除后不应再命中: %+v", hits)
	}
}

// Test_F51_KeywordHitsProperNoun 验收：专有名词由关键词一路命中。
func Test_F51_KeywordHitsProperNoun(t *testing.T) {
	t.Parallel()
	h := NewHybridRetriever(HybridConfig{})
	h.Add(1, "北京烤鸭店的招牌菜", f51Vec(1))
	h.Add(2, "今天天气不错适合散步", f51Vec(1))
	hits, deg := h.Search(context.Background(), "北京烤鸭", f51Vec(-1), 5)
	if len(hits) == 0 || hits[0].ID != 1 {
		t.Fatalf("专有名词查询应返回 id=1: %+v", hits)
	}
	if hits[0].KeywordRank != 1 {
		t.Fatalf("应由关键词一路命中: %+v", hits[0])
	}
	if deg.Any() {
		t.Fatalf("正常检索不应降级: %s", deg.Reason())
	}
}

// Test_F51_VectorHitsSynonym 验收：同义改写由向量一路命中。
func Test_F51_VectorHitsSynonym(t *testing.T) {
	t.Parallel()
	h := NewHybridRetriever(HybridConfig{})
	h.Add(1, "番茄炒蛋的做法", f51Vec(1))
	h.Add(2, "量子力学的入门教材", f51Vec(-1))
	hits, _ := h.Search(context.Background(), "西红柿", f51Vec(1), 5)
	if len(hits) == 0 || hits[0].ID != 1 {
		t.Fatalf("同义改写应靠向量命中 id=1: %+v", hits)
	}
	if hits[0].VectorRank == 0 {
		t.Fatalf("应由向量一路命中: %+v", hits[0])
	}
}

// Test_F51_DegradesWhenKeywordFails 边界：关键词一路失败时降级为向量单路。
func Test_F51_DegradesWhenKeywordFails(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{err: errors.New("倒排不可用")}
	vec := stubVector{hits: []VectorHit{{ID: 7, Text: "仅向量", Distance: 0}}}
	h := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{})
	hits, deg := h.Search(context.Background(), "x", nil, 5)
	if len(hits) != 1 || hits[0].ID != 7 {
		t.Fatalf("单路失败仍应返回向量结果: %+v", hits)
	}
	if deg.KeywordErr == "" || deg.VectorErr != "" {
		t.Fatalf("应只记录关键词一路失败: %+v", deg)
	}
	if !strings.Contains(deg.Reason(), "keyword:") {
		t.Fatalf("降级原因应可读: %q", deg.Reason())
	}
}

// Test_F51_DegradesWhenVectorFails 边界：向量一路失败时降级为关键词单路。
func Test_F51_DegradesWhenVectorFails(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{hits: []KeywordHit{{ID: 3, Text: "仅关键词", Score: 1}}}
	vec := stubVector{err: errors.New("向量库不可用")}
	h := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{})
	hits, deg := h.Search(context.Background(), "x", nil, 5)
	if len(hits) != 1 || hits[0].ID != 3 {
		t.Fatalf("单路失败仍应返回关键词结果: %+v", hits)
	}
	if deg.VectorErr == "" || deg.KeywordErr != "" {
		t.Fatalf("应只记录向量一路失败: %+v", deg)
	}
}

// Test_F51_BothPathsFailReturnsEmptyWithReasons 两路都失败也不整体失败。
func Test_F51_BothPathsFailReturnsEmptyWithReasons(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{err: errors.New("kw down")}
	vec := stubVector{err: errors.New("vec down")}
	h := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{})
	hits, deg := h.Search(context.Background(), "x", nil, 5)
	if len(hits) != 0 {
		t.Fatalf("两路都失败应返回空: %+v", hits)
	}
	if !deg.Any() {
		t.Fatalf("应标记降级")
	}
	reason := deg.Reason()
	if !strings.Contains(reason, "keyword:") || !strings.Contains(reason, "vector:") {
		t.Fatalf("应同时记录两路原因: %q", reason)
	}
}

// Test_F51_FusionDedupesAndRanksByRRF 验证 RRF 融合、去重与排序。
func Test_F51_FusionDedupesAndRanksByRRF(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{hits: []KeywordHit{
		{ID: 1, Text: "a", Score: 3},
		{ID: 2, Text: "b", Score: 2},
	}}
	vec := stubVector{hits: []VectorHit{
		{ID: 2, Text: "b", Distance: 1},
		{ID: 3, Text: "c", Distance: 2},
	}}
	h := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{RRFK: 60})
	hits, deg := h.Search(context.Background(), "q", nil, 5)
	if deg.Any() {
		t.Fatalf("无故障不应降级: %s", deg.Reason())
	}
	if len(hits) != 3 {
		t.Fatalf("融合后应去重为 3 条: %+v", hits)
	}
	// id2 两路都命中，融合分 = 1/62 + 1/61 最高；id1 与 id3 各命中一路。
	if hits[0].ID != 2 || hits[0].KeywordRank != 2 || hits[0].VectorRank != 1 || hits[0].Distance != 1 {
		t.Fatalf("id2 应携带两路排名且排第一: %+v", hits[0])
	}
	if hits[1].ID != 1 || hits[2].ID != 3 {
		t.Fatalf("排序应为 [2,1,3]: %+v", hits)
	}
	if hits[1].Distance != -1 {
		t.Fatalf("纯关键词命中距离应为 -1: %+v", hits[1])
	}
}

// Test_F51_RerankerApplied 验证可选重排对 Top 候选生效。
func Test_F51_RerankerApplied(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{hits: []KeywordHit{{ID: 1}, {ID: 2}, {ID: 3}}}
	vec := stubVector{}
	h := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{Reranker: reverseReranker{}})
	hits, deg := h.Search(context.Background(), "q", nil, 5)
	if deg.RerankErr != "" {
		t.Fatalf("重排不应失败: %s", deg.RerankErr)
	}
	if len(hits) != 3 || hits[0].ID != 3 || hits[2].ID != 1 {
		t.Fatalf("重排应反转顺序: %+v", hits)
	}
}

// Test_F51_RerankerFailureFallsBack 验证重排失败回退到融合顺序。
func Test_F51_RerankerFailureFallsBack(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{hits: []KeywordHit{{ID: 1}, {ID: 2}}}
	h := NewHybridRetrieverWithSearchers(kw, stubVector{}, HybridConfig{Reranker: brokenReranker{}})
	hits, deg := h.Search(context.Background(), "q", nil, 5)
	if deg.RerankErr == "" {
		t.Fatalf("应记录重排失败")
	}
	if len(hits) != 2 || hits[0].ID != 1 {
		t.Fatalf("重排失败应保留融合顺序: %+v", hits)
	}
}

// brokenReranker 固定失败。
type brokenReranker struct{}

func (brokenReranker) Rerank(context.Context, string, []HybridHit) ([]HybridHit, error) {
	return nil, errors.New("rerank down")
}

// Test_F51_WeightsAreConfigurable 验证两路权重可配且影响结果顺序。
func Test_F51_WeightsAreConfigurable(t *testing.T) {
	t.Parallel()
	kw := stubKeyword{hits: []KeywordHit{{ID: 1, Text: "kw"}}}
	vec := stubVector{hits: []VectorHit{{ID: 2, Text: "vec", Distance: 0}}}
	first := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{KeywordWeight: 5, VectorWeight: 1})
	hits, _ := first.Search(context.Background(), "q", nil, 5)
	if len(hits) != 2 || hits[0].ID != 1 {
		t.Fatalf("关键词权重更高时应排 id1: %+v", hits)
	}
	second := NewHybridRetrieverWithSearchers(kw, vec, HybridConfig{KeywordWeight: 1, VectorWeight: 5})
	hits, _ = second.Search(context.Background(), "q", nil, 5)
	if len(hits) != 2 || hits[0].ID != 2 {
		t.Fatalf("向量权重更高时应排 id2: %+v", hits)
	}
}

// Test_F51_VectorIndexSearcherAndEmpty 覆盖适配器与空索引路径。
func Test_F51_VectorIndexSearcherAndEmpty(t *testing.T) {
	t.Parallel()
	if _, err := (VectorIndexSearcher{}).Search(nil, 5); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("空索引适配器应报 ErrUnavailable: %v", err)
	}
	idx := vector.NewIndex()
	idx.Add(1, "doc", f51Vec(1))
	searcher := VectorIndexSearcher{Index: idx}
	hits, err := searcher.Search(f51Vec(1), 5)
	if err != nil || len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("适配器检索失败: hits=%+v err=%v", hits, err)
	}
	// 空检索器（两路都无结果）不报错。
	h := NewHybridRetrieverWithSearchers(stubKeyword{}, stubVector{}, HybridConfig{})
	got, deg := h.Search(context.Background(), "q", nil, 5)
	if len(got) != 0 || deg.Any() {
		t.Fatalf("空结果不算失败: hits=%+v deg=%+v", got, deg)
	}
}

// BenchmarkF51HybridSearch 用 1 万篇文档度量两路融合检索的开销。
func BenchmarkF51HybridSearch(b *testing.B) {
	const n = 10000
	h := NewHybridRetriever(HybridConfig{})
	v := f51Vec(1)
	for i := 0; i < n; i++ {
		h.Add(int64(i), "文档内容-北京烤鸭-"+string(rune('a'+i%26)), v)
	}
	q := f51Vec(1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if hits, _ := h.Search(context.Background(), "北京烤鸭", q, 10); len(hits) == 0 {
			b.Fatalf("应有命中")
		}
	}
}
