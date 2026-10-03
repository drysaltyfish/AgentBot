package memory

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// F-52 摘要树（RAPTOR 式）的默认参数。
const (
	// DefaultTreeMaxLevels 是自叶子向上最多再构建几层（collapse 次数）。
	DefaultTreeMaxLevels = 3
	// DefaultTreeMinCluster 是成簇下限；< 2 会退化成单链，故强制不小于 2。
	DefaultTreeMinCluster = 2
	// DefaultTreeBranching 是单簇的节点数上限，即分支因子。
	DefaultTreeBranching = 4
	// DefaultTreeMaxNodeLen 是单个节点（含生成摘要）的字符数上限。
	DefaultTreeMaxNodeLen = 2000
	// DefaultTreeMaxNodes 是全树节点数上限，防止无界增长。
	DefaultTreeMaxNodes = 10000
	// DefaultTreeTimeout 是单次构建（或续建）的总体时间预算。
	DefaultTreeTimeout = 30 * time.Second
	// DefaultClusterThreshold 是贪心聚类时归入既有簇的最低相似度。
	DefaultClusterThreshold = 0.5
)

// ErrTreeEmpty 表示没有可构建摘要树的文档块。
var ErrTreeEmpty = errors.New("summary tree: no chunks")

// SummaryNode 是摘要树的一个节点（F-52）。
//
// 叶子节点的 Content 是原始文档块；上层节点的 Summary 是簇摘要。
// Parent==0 表示根；Children 存的是节点 ID，避免指针环并让节点可复制。
type SummaryNode struct {
	ID       int64
	Level    int
	Content  string
	Summary  string
	Vector   []float32
	Children []int64
	Parent   int64
	Refs     []string
}

// IndexText 返回检索该节点时应比较的文本：优先摘要，其次原文。
func (n SummaryNode) IndexText() string {
	if strings.TrimSpace(n.Summary) != "" {
		return n.Summary
	}
	return n.Content
}

// Summarizer 把一个簇的文本压成一条摘要（F-52）。
//
// 抽成接口是刻意的：摘要必须由调用方注入，本包不硬编码任何模型调用。
// 返回错误表示该簇摘要失败，构建会退化为"拼接 + 截断"而不是中断。
type Summarizer interface {
	Summarize(ctx context.Context, texts []string) (string, error)
}

// Embedder 把文本转成浮点向量，供按相似度聚类使用（F-52）。
//
// 为 nil 时聚类退化为"按输入顺序定长切块"，构建仍然可用且确定。
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// JoinSummarizer 是不调用模型的确定性摘要器：直接拼接簇内文本。
//
// 它既是"未注入 Summarizer 时"的降级实现，也可在测试里显式注入。
type JoinSummarizer struct {
	// Separator 是拼接分隔符；空串表示换行。
	Separator string
}

// Summarize 实现 Summarizer。
func (j JoinSummarizer) Summarize(_ context.Context, texts []string) (string, error) {
	sep := j.Separator
	if sep == "" {
		sep = "\n"
	}
	return strings.Join(texts, sep), nil
}

// Chunk 是一个叶子文档块。
type Chunk struct {
	Text string
	Refs []string
}

// TreeOptions 是构建摘要树的配置；零值经归一化后即规格默认值。
type TreeOptions struct {
	// MaxLevels <=0 时用 DefaultTreeMaxLevels。
	MaxLevels int
	// MinCluster <2 时用 DefaultTreeMinCluster（保证不出现单链）。
	MinCluster int
	// Branching <2 时用 DefaultTreeBranching。
	Branching int
	// MaxNodeLen <=0 时用 DefaultTreeMaxNodeLen。
	MaxNodeLen int
	// MaxNodes <=0 时用 DefaultTreeMaxNodes。
	MaxNodes int
	// Timeout <=0 时用 DefaultTreeTimeout。
	Timeout time.Duration
	// ClusterThreshold <0 时用 DefaultClusterThreshold。
	ClusterThreshold float64
	// Summarizer 为 nil 时使用确定性的 JoinSummarizer 降级。
	Summarizer Summarizer
	// Embedder 为 nil 时不做向量化、按输入顺序聚类。
	Embedder Embedder
	// Similarity 是聚类用的相似度函数；为 nil 时使用余弦相似度。
	Similarity func(a, b []float32) float64
	// Warn 接收降级告警（摘要失败、embedding 失败）。
	Warn func(string)
}

// normalized 回填零值字段。
func (o TreeOptions) normalized() TreeOptions {
	if o.MaxLevels <= 0 {
		o.MaxLevels = DefaultTreeMaxLevels
	}
	if o.MinCluster < 2 {
		o.MinCluster = DefaultTreeMinCluster
	}
	if o.Branching < 2 {
		o.Branching = DefaultTreeBranching
	}
	if o.MaxNodeLen <= 0 {
		o.MaxNodeLen = DefaultTreeMaxNodeLen
	}
	if o.MaxNodes <= 0 {
		o.MaxNodes = DefaultTreeMaxNodes
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTreeTimeout
	}
	if o.ClusterThreshold < 0 {
		o.ClusterThreshold = DefaultClusterThreshold
	}
	return o
}

// BuildReport 是一次构建的进度记录，也是断点续建的依据（F-52）。
type BuildReport struct {
	// Levels 是有节点的层数（含叶子层）。
	Levels int
	// Nodes/Leaves 是当前树的节点总数与叶子数。
	Nodes  int
	Leaves int
	// LastCompletedLevel 是最后一个**完整**建成的层级；续建从它的上一层开始。
	LastCompletedLevel int
	// FailedClusters 记录摘要失败并已降级的簇（簇内 ID 升序拼接）。
	FailedClusters []string
	// SkippedClusters 记录 embedding 失败而被跳过的簇。
	SkippedClusters []string
	// TimedOut/Canceled 记录构建为何提前停止。
	TimedOut   bool
	Canceled   bool
	CapReached bool
	// Err 是停止原因的可读文本；正常完成为空。
	Err string
}

// SummaryTree 是 RAPTOR 式递归摘要树；零值不可用，请用 NewSummaryTree 构造。
//
// 构建由 buildMu 串行化；检索与读取用 RWMutex 保护，可并发。
type SummaryTree struct {
	opts TreeOptions

	buildMu sync.Mutex

	mu      sync.RWMutex
	nodes   map[int64]*SummaryNode
	byLevel map[int][]int64
	nextID  int64
	report  BuildReport
}

// NewSummaryTree 用配置构造一棵空树。
func NewSummaryTree(opts TreeOptions) *SummaryTree {
	return &SummaryTree{
		opts:    opts.normalized(),
		nodes:   make(map[int64]*SummaryNode),
		byLevel: make(map[int][]int64),
	}
}

// Build 从文档块构建摘要树；树非空时等价于 Resume。
//
// 摘要失败降级、embedding 失败跳过该簇，均不返回错误；停止原因在 BuildReport 里。
func (t *SummaryTree) Build(ctx context.Context, chunks []Chunk) BuildReport {
	if t == nil {
		return BuildReport{Err: ErrUnavailable.Error()}
	}
	t.buildMu.Lock()
	defer t.buildMu.Unlock()

	t.mu.RLock()
	empty := len(t.nodes) == 0
	t.mu.RUnlock()
	if !empty {
		return t.resumeLocked(ctx)
	}
	if len(chunks) == 0 {
		rep := BuildReport{Err: ErrTreeEmpty.Error()}
		t.store(rep)
		return rep
	}

	local := make(map[int64]*SummaryNode, len(chunks))
	byLevel := make(map[int][]int64)
	nextID := int64(0)
	level0 := make([]int64, 0, len(chunks))
	for _, ch := range chunks {
		nextID++
		text := strings.TrimSpace(ch.Text)
		n := &SummaryNode{ID: nextID, Level: 0, Content: text, Refs: slices.Clone(ch.Refs)}
		if t.opts.Embedder != nil {
			if v, err := t.opts.Embedder.Embed(ctx, text); err == nil {
				n.Vector = v
			} else if t.opts.Warn != nil {
				t.opts.Warn("summary tree: leaf embedding failed: " + err.Error())
			}
		}
		local[nextID] = n
		level0 = append(level0, nextID)
	}
	byLevel[0] = level0
	report := BuildReport{Leaves: len(level0), Nodes: len(local), LastCompletedLevel: 0}

	runCtx, cancel := context.WithTimeout(ctx, t.opts.Timeout)
	defer cancel()
	report = t.buildFrom(runCtx, local, byLevel, level0, 1, &nextID, report)
	report.Levels = len(byLevel)
	t.storeWith(local, byLevel, nextID, report)
	return report
}

// Resume 从最后一个完整层级继续构建；已完成的层级不会重算。
func (t *SummaryTree) Resume(ctx context.Context) BuildReport {
	if t == nil {
		return BuildReport{Err: ErrUnavailable.Error()}
	}
	t.buildMu.Lock()
	defer t.buildMu.Unlock()
	return t.resumeLocked(ctx)
}

// resumeLocked 在持有 buildMu 的前提下续建。
func (t *SummaryTree) resumeLocked(ctx context.Context) BuildReport {
	t.mu.RLock()
	local := cloneNodeMap(t.nodes)
	byLevel := cloneLevelMap(t.byLevel)
	nextID := t.nextID
	report := t.report
	t.mu.RUnlock()

	report.TimedOut = false
	report.Canceled = false
	report.CapReached = false
	report.Err = ""

	startLevel := maxLevel(byLevel) + 1
	var current []int64
	if startLevel > 0 {
		current = byLevel[startLevel-1]
	}
	runCtx, cancel := context.WithTimeout(ctx, t.opts.Timeout)
	defer cancel()
	report = t.buildFrom(runCtx, local, byLevel, current, startLevel, &nextID, report)
	report.Levels = len(byLevel)
	report.Nodes = len(local)
	t.storeWith(local, byLevel, nextID, report)
	return report
}

// buildFrom 从 startLevel 起逐层构建，每层要么整层提交、要么整层丢弃，
// 从而保证 LastCompletedLevel 永远是可续建的完整层级。
func (t *SummaryTree) buildFrom(ctx context.Context, local map[int64]*SummaryNode, byLevel map[int][]int64, current []int64, startLevel int, nextID *int64, report BuildReport) BuildReport {
	for level := startLevel; level <= t.opts.MaxLevels; level++ {
		if len(current) < t.opts.MinCluster {
			break
		}
		if len(local) >= t.opts.MaxNodes {
			report.CapReached = true
			report.Err = "summary tree: max nodes reached"
			break
		}
		res := t.buildLevel(ctx, local, current, level)
		if res.stop != nil {
			if errors.Is(res.stop, context.DeadlineExceeded) {
				report.TimedOut = true
			}
			if errors.Is(res.stop, context.Canceled) {
				report.Canceled = true
			}
			report.Err = res.stop.Error()
			break
		}
		if res.capReached {
			report.CapReached = true
			report.Err = "summary tree: max nodes reached"
			break
		}
		report.FailedClusters = append(report.FailedClusters, res.failed...)
		report.SkippedClusters = append(report.SkippedClusters, res.skipped...)
		ids := commitLevel(local, byLevel, nextID, level, res.pending)
		if len(ids) == 0 {
			break
		}
		report.LastCompletedLevel = level
		current = ids
	}
	report.Nodes = len(local)
	report.Levels = len(byLevel)
	return report
}

// levelBuild 是一层构建的中间结果；pending 只有在整层成功后才提交。
type levelBuild struct {
	pending    []*SummaryNode
	failed     []string
	skipped    []string
	stop       error
	capReached bool
}

// buildLevel 构建一层：聚类 -> 摘要 -> embedding；任一簇失败只影响该簇。
func (t *SummaryTree) buildLevel(ctx context.Context, local map[int64]*SummaryNode, current []int64, level int) levelBuild {
	var res levelBuild
	for _, cluster := range t.cluster(local, current) {
		if err := ctx.Err(); err != nil {
			res.stop = err
			return res
		}
		if len(local)+len(res.pending) >= t.opts.MaxNodes {
			res.capReached = true
			return res
		}
		texts := make([]string, 0, len(cluster))
		for _, id := range cluster {
			texts = append(texts, local[id].IndexText())
		}
		summary, failed := t.summarizeCluster(ctx, texts)
		if failed {
			res.failed = append(res.failed, clusterKey(cluster))
			if t.opts.Warn != nil {
				t.opts.Warn("summary tree: cluster summary failed; degraded to join+truncate: " + clusterKey(cluster))
			}
		}
		summary = truncateRunes(summary, t.opts.MaxNodeLen)
		var vec []float32
		if t.opts.Embedder != nil {
			v, err := t.opts.Embedder.Embed(ctx, summary)
			if err != nil {
				// 边界：embedding 失败必须跳过该簇，不影响其他簇。
				res.skipped = append(res.skipped, clusterKey(cluster))
				if t.opts.Warn != nil {
					t.opts.Warn("summary tree: cluster embedding failed; skipped: " + clusterKey(cluster))
				}
				continue
			}
			vec = v
		}
		res.pending = append(res.pending, &SummaryNode{
			Level:    level,
			Summary:  summary,
			Vector:   vec,
			Children: slices.Clone(cluster),
			Refs:     unionRefs(local, cluster),
		})
	}
	return res
}

// summarizeCluster 生成簇摘要；无注入时走确定性降级，失败时同样降级并标记。
func (t *SummaryTree) summarizeCluster(ctx context.Context, texts []string) (string, bool) {
	if t.opts.Summarizer == nil {
		return fallbackSummary(texts), false
	}
	s, err := t.opts.Summarizer.Summarize(ctx, texts)
	if err != nil || strings.TrimSpace(s) == "" {
		return fallbackSummary(texts), true
	}
	return s, false
}

// cluster 把当前层节点聚成簇。有向量时按质心相似度贪心聚类，
// 否则按输入顺序定长切块；均保证单簇不超过 Branching、小于 MinCluster 的簇被丢弃。
func (t *SummaryTree) cluster(local map[int64]*SummaryNode, current []int64) [][]int64 {
	canCluster := true
	for _, id := range current {
		if len(local[id].Vector) == 0 {
			canCluster = false
			break
		}
	}
	if !canCluster {
		return filterClusters(chunkIDs(current, t.opts.Branching), t.opts.MinCluster)
	}
	sim := t.opts.Similarity
	if sim == nil {
		sim = cosineSimilarity
	}
	var clusters [][]int64
	var centroids [][]float32
	for _, id := range current {
		v := local[id].Vector
		best, bestSim := -1, 0.0
		for i := range clusters {
			if len(clusters[i]) >= t.opts.Branching {
				continue
			}
			s := sim(v, centroids[i])
			if s >= t.opts.ClusterThreshold && (best < 0 || s > bestSim) {
				best, bestSim = i, s
			}
		}
		if best < 0 {
			clusters = append(clusters, []int64{id})
			centroids = append(centroids, slices.Clone(v))
			continue
		}
		clusters[best] = append(clusters[best], id)
		centroids[best] = mergeCentroid(centroids[best], v, len(clusters[best]))
	}
	return filterClusters(clusters, t.opts.MinCluster)
}

// commitLevel 给一层的新节点分配 ID、回填父指针并写入树结构。
func commitLevel(local map[int64]*SummaryNode, byLevel map[int][]int64, nextID *int64, level int, pending []*SummaryNode) []int64 {
	ids := make([]int64, 0, len(pending))
	for _, n := range pending {
		*nextID++
		n.ID = *nextID
		for _, cid := range n.Children {
			if child, ok := local[cid]; ok {
				child.Parent = n.ID
			}
		}
		local[n.ID] = n
		byLevel[level] = append(byLevel[level], n.ID)
		ids = append(ids, n.ID)
	}
	return ids
}

// Search 在**每一层**做词法检索并合并去重（F-52 的检索规格）。
//
// 排序：分数降序 -> 层级降序（宏观问题先命中上层摘要）-> ID 升序，保证确定性。
func (t *SummaryTree) Search(query string, k int) []TreeHit {
	if t == nil || k <= 0 {
		return nil
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	hits := make([]TreeHit, 0, len(t.nodes))
	for _, id := range t.sortedIDsLocked() {
		n := t.nodes[id]
		if n == nil {
			continue
		}
		score := lexicalScore(q, n.IndexText())
		if score <= 0 {
			continue
		}
		hits = append(hits, TreeHit{ID: n.ID, Level: n.Level, Text: n.IndexText(), Score: score})
	}
	return topHits(hits, k)
}

// SearchVector 用注入的相似度（默认余弦）在带向量的节点里检索，同样跨层合并。
func (t *SummaryTree) SearchVector(qv []float32, k int) []TreeHit {
	if t == nil || k <= 0 || len(qv) == 0 {
		return nil
	}
	sim := t.opts.Similarity
	if sim == nil {
		sim = cosineSimilarity
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	hits := make([]TreeHit, 0, len(t.nodes))
	for _, id := range t.sortedIDsLocked() {
		n := t.nodes[id]
		if n == nil || len(n.Vector) == 0 {
			continue
		}
		s := sim(qv, n.Vector)
		if s <= 0 {
			continue
		}
		hits = append(hits, TreeHit{ID: n.ID, Level: n.Level, Text: n.IndexText(), Score: s})
	}
	return topHits(hits, k)
}

// TreeHit 是一条摘要树检索结果。
type TreeHit struct {
	ID    int64
	Level int
	Text  string
	Score float64
}

// topHits 做全序排序并截断到 k。
func topHits(hits []TreeHit, k int) []TreeHit {
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		if hits[a].Level != hits[b].Level {
			return hits[a].Level > hits[b].Level
		}
		return hits[a].ID < hits[b].ID
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// sortedIDsLocked 返回按 ID 升序的全部节点 ID；调用方持锁。
func (t *SummaryTree) sortedIDsLocked() []int64 {
	ids := make([]int64, 0, len(t.nodes))
	for id := range t.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}

// Levels 返回有节点的层数（含叶子层）。
func (t *SummaryTree) Levels() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byLevel)
}

// Len 返回节点总数。
func (t *SummaryTree) Len() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.nodes)
}

// Node 返回节点的深拷贝。
func (t *SummaryTree) Node(id int64) (SummaryNode, bool) {
	if t == nil {
		return SummaryNode{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[id]
	if !ok {
		return SummaryNode{}, false
	}
	return *cloneNode(n), true
}

// NodesAtLevel 返回某层全部节点的深拷贝，按 ID 升序。
func (t *SummaryTree) NodesAtLevel(level int) []SummaryNode {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	ids := t.byLevel[level]
	out := make([]SummaryNode, 0, len(ids))
	for _, id := range ids {
		if n, ok := t.nodes[id]; ok {
			out = append(out, *cloneNode(n))
		}
	}
	return out
}

// Roots 返回 Parent==0 的节点（每棵树的最上层），按 ID 升序。
func (t *SummaryTree) Roots() []SummaryNode {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]SummaryNode, 0, len(t.nodes))
	for _, id := range t.sortedIDsLocked() {
		n := t.nodes[id]
		if n != nil && n.Parent == 0 {
			out = append(out, *cloneNode(n))
		}
	}
	return out
}

// Report 返回最近一次构建的进度记录。
func (t *SummaryTree) Report() BuildReport {
	if t == nil {
		return BuildReport{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneReport(t.report)
}

// store 只更新进度记录。
func (t *SummaryTree) store(rep BuildReport) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.report = rep
}

// storeWith 原子地替换树结构与进度记录。
func (t *SummaryTree) storeWith(nodes map[int64]*SummaryNode, byLevel map[int][]int64, nextID int64, rep BuildReport) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes = nodes
	t.byLevel = byLevel
	t.nextID = nextID
	t.report = rep
}

// chunkIDs 按固定大小顺序切块。
func chunkIDs(ids []int64, size int) [][]int64 {
	if size < 1 {
		size = 1
	}
	var out [][]int64
	for i := 0; i < len(ids); i += size {
		out = append(out, slices.Clone(ids[i:min(i+size, len(ids))]))
	}
	return out
}

// filterClusters 丢弃小于 minCluster 的簇：这既避免单链，也让零散节点留在原层。
func filterClusters(clusters [][]int64, minCluster int) [][]int64 {
	out := make([][]int64, 0, len(clusters))
	for _, c := range clusters {
		if len(c) >= minCluster {
			out = append(out, c)
		}
	}
	return out
}

// mergeCentroid 把 v 并入质心（运行平均），长度取原质心长度。
func mergeCentroid(c, v []float32, count int) []float32 {
	out := make([]float32, len(c))
	n := min(len(c), len(v))
	for i := range c {
		if i < n && count > 0 {
			out[i] = c[i] + (v[i]-c[i])/float32(count)
			continue
		}
		out[i] = c[i]
	}
	return out
}

// cosineSimilarity 是默认相似度：两向量夹角的余弦；任一为零向量时返回 0。
func cosineSimilarity(a, b []float32) float64 {
	n := min(len(a), len(b))
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// fallbackSummary 是摘要降级：拼接 + 截断（截断在调用处按配置执行）。
func fallbackSummary(texts []string) string {
	return strings.Join(texts, "\n")
}

// truncateRunes 按字符数截断；maxLen<=0 表示不截断。
func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

// clusterKey 生成簇的确定性标识。
func clusterKey(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// unionRefs 合并簇内节点的 Refs，保序去重。
func unionRefs(local map[int64]*SummaryNode, cluster []int64) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, id := range cluster {
		for _, r := range local[id].Refs {
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			out = append(out, r)
		}
	}
	return out
}

// maxLevel 返回已提交的最高层级；空树为 -1。
func maxLevel(byLevel map[int][]int64) int {
	top := -1
	for level := range byLevel {
		if level > top {
			top = level
		}
	}
	return top
}

// cloneNode 深拷贝节点。
func cloneNode(n *SummaryNode) *SummaryNode {
	if n == nil {
		return nil
	}
	c := *n
	c.Vector = slices.Clone(n.Vector)
	c.Children = slices.Clone(n.Children)
	c.Refs = slices.Clone(n.Refs)
	return &c
}

// cloneNodeMap 深拷贝节点表。
func cloneNodeMap(in map[int64]*SummaryNode) map[int64]*SummaryNode {
	out := make(map[int64]*SummaryNode, len(in))
	for id, n := range in {
		out[id] = cloneNode(n)
	}
	return out
}

// cloneLevelMap 深拷贝层级索引。
func cloneLevelMap(in map[int][]int64) map[int][]int64 {
	out := make(map[int][]int64, len(in))
	for level, ids := range in {
		out[level] = slices.Clone(ids)
	}
	return out
}

// cloneReport 拷贝进度记录，避免调用方改到内部切片。
func cloneReport(rep BuildReport) BuildReport {
	rep.FailedClusters = slices.Clone(rep.FailedClusters)
	rep.SkippedClusters = slices.Clone(rep.SkippedClusters)
	return rep
}
