package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/drysaltyfish/agentbot/internal/vector"
)

// tokenize 把文本切成检索用 token：
//
//   - ASCII/其它字母数字连续段整体小写后作为一个 token（命令、ID、英文词）；
//   - 中日韩统一表意文字连续段切成"单字 + 相邻二字组"（中文无需分词即可召回）。
//
// 同时保留单字与二字组：单字让"猫"这类极短查询也能命中，
// 二字组提供"北京烤鸭"这类专有名词的区分度；常见字由 BM25 的 IDF 自然降权。
func tokenize(s string) []string {
	var out []string
	var word []rune
	var han []rune

	flushWord := func() {
		if len(word) == 0 {
			return
		}
		out = append(out, strings.ToLower(string(word)))
		word = word[:0]
	}
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		for _, r := range han {
			out = append(out, string(r))
		}
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

// uniqueTokens 返回去重后的 token；保持首次出现顺序，保证确定性。
func uniqueTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// KeywordHit 是关键词一路的检索结果。
type KeywordHit struct {
	ID    int64
	Text  string
	Score float64
}

// VectorHit 是向量一路的检索结果。
type VectorHit struct {
	ID       int64
	Text     string
	Distance int
}

// KeywordSearcher 是关键词/BM25 一路的检索接口。
//
// 抽成接口是为了让"这一路失败"可被注入与观测（F-51 的降级边界）。
type KeywordSearcher interface {
	Search(query string, k int) ([]KeywordHit, error)
}

// VectorSearcher 是向量一路的检索接口。
type VectorSearcher interface {
	Search(q []float32, k int) ([]VectorHit, error)
}

// VectorIndexSearcher 把 internal/vector.Index 适配成 VectorSearcher。
type VectorIndexSearcher struct {
	Index *vector.Index
}

// Search 实现 VectorSearcher。
func (s VectorIndexSearcher) Search(q []float32, k int) ([]VectorHit, error) {
	if s.Index == nil {
		return nil, ErrUnavailable
	}
	hits := s.Index.Search(q, k)
	out := make([]VectorHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, VectorHit{ID: h.ID, Text: h.Text, Distance: h.Distance})
	}
	return out, nil
}

// BM25 的默认参数。
const (
	// DefaultBM25K1 是词频饱和系数。
	DefaultBM25K1 = 1.2
	// DefaultBM25B 是文档长度归一化系数。
	DefaultBM25B = 0.75
)

// BM25Index 是内存倒排索引 + BM25 打分，支持并发读写；零值可直接使用。
type BM25Index struct {
	mu       sync.RWMutex
	docs     map[int64]string
	postings map[string]map[int64]int
	df       map[string]int
	lengths  map[int64]int
	totalLen int
	k1       float64
	b        float64
}

// NewBM25Index 返回一个空索引。
func NewBM25Index() *BM25Index {
	return &BM25Index{
		docs:     make(map[int64]string),
		postings: make(map[string]map[int64]int),
		df:       make(map[string]int),
		lengths:  make(map[int64]int),
		k1:       DefaultBM25K1,
		b:        DefaultBM25B,
	}
}

// ensure 初始化零值索引；调用方必须持锁。
func (x *BM25Index) ensure() {
	if x.docs == nil {
		x.docs = make(map[int64]string)
	}
	if x.postings == nil {
		x.postings = make(map[string]map[int64]int)
	}
	if x.df == nil {
		x.df = make(map[string]int)
	}
	if x.lengths == nil {
		x.lengths = make(map[int64]int)
	}
	if x.k1 <= 0 {
		x.k1 = DefaultBM25K1
	}
	if x.b <= 0 {
		x.b = DefaultBM25B
	}
}

// Add 写入或覆盖文档 id。
func (x *BM25Index) Add(id int64, text string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensure()
	if _, ok := x.docs[id]; ok {
		x.removeLocked(id)
	}
	tokens := tokenize(text)
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	x.docs[id] = text
	x.lengths[id] = len(tokens)
	x.totalLen += len(tokens)
	for t, n := range tf {
		p := x.postings[t]
		if p == nil {
			p = make(map[int64]int)
			x.postings[t] = p
		}
		p[id] = n
		x.df[t]++
	}
}

// Remove 删除文档，返回是否真的删除了。
func (x *BM25Index) Remove(id int64) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensure()
	if _, ok := x.docs[id]; !ok {
		return false
	}
	x.removeLocked(id)
	return true
}

// removeLocked 在持写锁的前提下删除文档并同步倒排与文档频率。
func (x *BM25Index) removeLocked(id int64) {
	text := x.docs[id]
	seen := make(map[string]struct{})
	for _, t := range tokenize(text) {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		if p := x.postings[t]; p != nil {
			delete(p, id)
			if len(p) == 0 {
				delete(x.postings, t)
			}
		}
		if x.df[t] > 0 {
			x.df[t]--
			if x.df[t] == 0 {
				delete(x.df, t)
			}
		}
	}
	x.totalLen -= x.lengths[id]
	delete(x.lengths, id)
	delete(x.docs, id)
}

// Len 返回文档数。
func (x *BM25Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.docs)
}

// Search 实现 KeywordSearcher：按 BM25 分数降序返回至多 k 条；同分按 ID 升序。
//
// k<=0 或查询无 token 时返回 nil。异常情况经 error 返回，
// 便于上层把"关键词一路失败"与"没有命中"分开处理。
func (x *BM25Index) Search(query string, k int) ([]KeywordHit, error) {
	if k <= 0 {
		return nil, nil
	}
	tokens := uniqueTokens(tokenize(query))
	if len(tokens) == 0 {
		return nil, nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	if len(x.docs) == 0 {
		return nil, nil
	}
	n := float64(len(x.docs))
	avgLen := 1.0
	if x.totalLen > 0 {
		avgLen = float64(x.totalLen) / n
	}
	scores := make(map[int64]float64)
	for _, t := range tokens {
		df := float64(x.df[t])
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for id, tf := range x.postings[t] {
			dl := float64(x.lengths[id])
			denom := float64(tf) + x.k1*(1-x.b+x.b*dl/avgLen)
			if denom <= 0 {
				continue
			}
			scores[id] += idf * float64(tf) * (x.k1 + 1) / denom
		}
	}
	if len(scores) == 0 {
		return nil, nil
	}
	hits := make([]KeywordHit, 0, len(scores))
	for id, score := range scores {
		hits = append(hits, KeywordHit{ID: id, Text: x.docs[id], Score: score})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].ID < hits[b].ID
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// F-51 的默认融合参数。
const (
	// DefaultRRFK 是 RRF 的平滑常数。
	DefaultRRFK = 60
	// DefaultHybridTopK 是最终返回条数。
	DefaultHybridTopK = 5
	// DefaultCandidateK 是每路（及重排）候选数。
	DefaultCandidateK = 20
	// DefaultVectorRadius 是向量检索的相邻桶半径。
	DefaultVectorRadius = 1
)

// Reranker 可选地重排融合后的 Top 候选（规则或小模型）。
type Reranker interface {
	Rerank(ctx context.Context, query string, hits []HybridHit) ([]HybridHit, error)
}

// HybridConfig 是混合检索的配置；零值经归一化后即规格默认值。
type HybridConfig struct {
	// RRFK <=0 时用 DefaultRRFK。
	RRFK int
	// KeywordWeight/VectorWeight <0 表示关闭该路；==0 时用 1.0。
	KeywordWeight float64
	VectorWeight  float64
	// TopK <=0 时用 DefaultHybridTopK。
	TopK int
	// CandidateK <=0 时用 DefaultCandidateK，且不小于 TopK。
	CandidateK int
	// VectorRadius <0 时用 DefaultVectorRadius。
	VectorRadius int
	// Reranker 可选：对 Top CandidateK 重排后取 TopK。
	Reranker Reranker
}

// normalized 回填零值。
func (c HybridConfig) normalized() HybridConfig {
	if c.RRFK <= 0 {
		c.RRFK = DefaultRRFK
	}
	if c.KeywordWeight == 0 {
		c.KeywordWeight = 1
	}
	if c.VectorWeight == 0 {
		c.VectorWeight = 1
	}
	if c.TopK <= 0 {
		c.TopK = DefaultHybridTopK
	}
	if c.CandidateK <= 0 {
		c.CandidateK = DefaultCandidateK
	}
	if c.CandidateK < c.TopK {
		c.CandidateK = c.TopK
	}
	if c.VectorRadius < 0 {
		c.VectorRadius = DefaultVectorRadius
	}
	return c
}

// HybridHit 是融合后的一条结果。ID/Text 与两路排名同属一个结构体，
// 排序时整体移动，从类型上杜绝并行数组错位。
type HybridHit struct {
	ID           int64
	Text         string
	Score        float64
	KeywordRank  int // 1 起；0 表示关键词一路未命中
	VectorRank   int // 1 起；0 表示向量一路未命中
	KeywordScore float64
	Distance     int // 向量距离；未命中向量时为 -1
}

// Degradation 记录本次检索的降级原因（F-51 边界：单路失败不整体失败）。
type Degradation struct {
	KeywordErr string
	VectorErr  string
	RerankErr  string
}

// Any 报告本次检索是否发生了任何降级。
func (d Degradation) Any() bool {
	return d.KeywordErr != "" || d.VectorErr != "" || d.RerankErr != ""
}

// Reason 返回可读的降级原因；无降级时为空串。
func (d Degradation) Reason() string {
	var parts []string
	if d.KeywordErr != "" {
		parts = append(parts, "keyword: "+d.KeywordErr)
	}
	if d.VectorErr != "" {
		parts = append(parts, "vector: "+d.VectorErr)
	}
	if d.RerankErr != "" {
		parts = append(parts, "rerank: "+d.RerankErr)
	}
	return strings.Join(parts, "; ")
}

// HybridRetriever 实现 F-51：BM25 + 二值向量两路召回，RRF 融合排序。
type HybridRetriever struct {
	cfg      HybridConfig
	bm25     *BM25Index
	vec      *vector.Index
	vecS     VectorIndexSearcher
	keyword  KeywordSearcher // 测试或外部注入；非空时覆盖 bm25
	override VectorSearcher  // 测试或外部注入；非空时覆盖 vec
}

// NewHybridRetriever 构造带内置 BM25 与向量索引的检索器。
func NewHybridRetriever(cfg HybridConfig) *HybridRetriever {
	bm := NewBM25Index()
	vec := vector.NewIndex()
	return &HybridRetriever{
		cfg:  cfg.normalized(),
		bm25: bm,
		vec:  vec,
		vecS: VectorIndexSearcher{Index: vec},
	}
}

// NewHybridRetrieverWithSearchers 用注入的两路检索器构造（用于替换实现或故障注入）。
func NewHybridRetrieverWithSearchers(keyword KeywordSearcher, vec VectorSearcher, cfg HybridConfig) *HybridRetriever {
	return &HybridRetriever{
		cfg:      cfg.normalized(),
		keyword:  keyword,
		override: vec,
	}
}

// Add 同时写入两路索引。
func (h *HybridRetriever) Add(id int64, text string, v []float32) {
	if h == nil {
		return
	}
	if h.bm25 != nil {
		h.bm25.Add(id, text)
	}
	if h.vec != nil {
		h.vec.Add(id, text, v)
	}
}

// Remove 同时从两路索引删除，返回是否真的删除了。
func (h *HybridRetriever) Remove(id int64) bool {
	if h == nil {
		return false
	}
	removed := false
	if h.bm25 != nil {
		removed = h.bm25.Remove(id) || removed
	}
	if h.vec != nil {
		removed = h.vec.Remove(id) || removed
	}
	return removed
}

// Len 返回内置两路索引中的文档数（注入模式下返回 0）。
func (h *HybridRetriever) Len() int {
	if h == nil || h.bm25 == nil {
		return 0
	}
	return h.bm25.Len()
}

// keywordSearcher 返回实际使用的关键词检索器。
func (h *HybridRetriever) keywordSearcher() KeywordSearcher {
	if h.keyword != nil {
		return h.keyword
	}
	return h.bm25
}

// vectorSearcher 返回实际使用的向量检索器。
func (h *HybridRetriever) vectorSearcher() VectorSearcher {
	if h.override != nil {
		return h.override
	}
	return h.vecS
}

// Search 执行两路召回并用 RRF 融合，返回按融合分降序的至多 k 条结果。
//
// k<=0 时使用配置的 TopK。任一路失败时降级为单路，并在 Degradation 里记录原因；
// 两路都失败时返回空结果与两条原因，而不是整体报错。
func (h *HybridRetriever) Search(ctx context.Context, query string, qv []float32, k int) ([]HybridHit, Degradation) {
	var deg Degradation
	if h == nil {
		return nil, deg
	}
	cfg := h.cfg.normalized()
	if k <= 0 {
		k = cfg.TopK
	}
	candidates := cfg.CandidateK

	keywordHits, vecHits := h.recall(cfg, query, qv, candidates, &deg)
	fused := fuseRRF(cfg, keywordHits, vecHits)
	if len(fused) == 0 {
		return nil, deg
	}
	if cfg.Reranker != nil {
		top := fused
		if len(top) > candidates {
			top = top[:candidates]
		}
		reranked, err := cfg.Reranker.Rerank(ctx, query, top)
		switch {
		case err != nil:
			deg.RerankErr = err.Error()
		case len(reranked) > 0:
			fused = reranked
		}
	}
	if len(fused) > k {
		fused = fused[:k]
	}
	return fused, deg
}

// recall 调用两路检索器，把失败记录到 deg，并把跳过的一路返回空。
func (h *HybridRetriever) recall(cfg HybridConfig, query string, qv []float32, candidates int, deg *Degradation) ([]KeywordHit, []VectorHit) {
	var keywordHits []KeywordHit
	if cfg.KeywordWeight >= 0 {
		if searcher := h.keywordSearcher(); searcher != nil {
			hits, err := searcher.Search(query, candidates)
			if err != nil {
				deg.KeywordErr = err.Error()
			} else {
				keywordHits = hits
			}
		}
	}
	var vecHits []VectorHit
	if cfg.VectorWeight >= 0 {
		if searcher := h.vectorSearcher(); searcher != nil {
			hits, err := searcher.Search(qv, candidates)
			if err != nil {
				deg.VectorErr = err.Error()
			} else {
				vecHits = hits
			}
		}
	}
	return keywordHits, vecHits
}

// fuseRRF 按 score = Σ weight/(k + rank) 融合两路结果，同一 ID 自动去重。
func fuseRRF(cfg HybridConfig, keywordHits []KeywordHit, vecHits []VectorHit) []HybridHit {
	fused := make(map[int64]*HybridHit, len(keywordHits)+len(vecHits))
	for i, hit := range keywordHits {
		rank := i + 1
		item := ensureFused(fused, hit.ID, hit.Text)
		item.Score += cfg.KeywordWeight / float64(cfg.RRFK+rank)
		item.KeywordRank = rank
		item.KeywordScore = hit.Score
	}
	for i, hit := range vecHits {
		rank := i + 1
		item := ensureFused(fused, hit.ID, hit.Text)
		item.Score += cfg.VectorWeight / float64(cfg.RRFK+rank)
		item.VectorRank = rank
		item.Distance = hit.Distance
	}
	out := make([]HybridHit, 0, len(fused))
	for _, item := range fused {
		if item.Distance == 0 && item.VectorRank == 0 {
			item.Distance = -1
		}
		out = append(out, *item)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// ensureFused 取回或创建融合条目；文本以先到者为准。
func ensureFused(fused map[int64]*HybridHit, id int64, text string) *HybridHit {
	if item, ok := fused[id]; ok {
		return item
	}
	item := &HybridHit{ID: id, Text: text, Distance: -1}
	fused[id] = item
	return item
}
