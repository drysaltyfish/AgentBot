package vector

import (
	"cmp"
	"math"
	"math/rand"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// f50Dim 是多数用例使用的向量维度，保证 64 维 = 8 字节的打包形态被覆盖。
const f50Dim = 64

// f50ScaledSign 生成 dim 维 ±1 向量并整体缩放到目标 L2 范数。
func f50ScaledSign(rng *rand.Rand, dim int, target float64) []float32 {
	v := make([]float32, dim)
	for i := range v {
		if rng.Intn(2) == 0 {
			v[i] = -1
		} else {
			v[i] = 1
		}
	}
	return f50ScaleTo(v, target)
}

// f50ScaleTo 把向量整体缩放到目标 L2 范数，保持各分量符号不变。
func f50ScaleTo(v []float32, target float64) []float32 {
	n := Norm(v)
	if n == 0 {
		n = 1
	}
	s := float32(target / n)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * s
	}
	return out
}

// f50NormFor 返回使 bucket=round(‖v‖×2) mod 64 恰为 g 的范数。
func f50NormFor(g int) float64 { return (float64(g) + 0.2) / 2.0 }

// f50SortHits 用与 Index 相同的全序排序，作为暴力对照。
func f50SortHits(hits []Hit) {
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := cmp.Compare(a.Distance, b.Distance); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// Test_F50_EncodeThresholdAndBytes 验证二值化阈值、8 位打包、Bytes/FromBytes 往返。
func Test_F50_EncodeThresholdAndBytes(t *testing.T) {
	t.Parallel()

	// 默认 vtb=0：正与零为 1，负为 0。
	b := Encode([]float32{1, 0.5, 0, -0.1, -2})
	if len(b) != 1 || b.Len() != 8 {
		t.Fatalf("5 维应打包成 1 字节 / 8 位，得到 %d / %d", len(b), b.Len())
	}
	if want := byte(0b00111); b[0] != want {
		t.Fatalf("量化结果 = %#b，期望 %#b", b[0], want)
	}

	// 64 维 = 8 字节。
	wide := Encode(make([]float32, 64))
	if len(wide) != 8 || wide.Len() != 64 {
		t.Fatalf("64 维应打包成 8 字节 / 64 位，得到 %d / %d", len(wide), wide.Len())
	}

	// 自定义阈值：仅 >=2 置 1。
	tb := EncodeThreshold([]float32{1, 2, 3}, 2)
	if tb[0] != 0b110 {
		t.Fatalf("阈值量化 = %#b，期望 %#b", tb[0], 0b110)
	}

	// Bytes 返回副本，改动不回写。
	raw := b.Bytes()
	raw[0] = 0xFF
	if b[0] == 0xFF {
		t.Fatalf("Bytes() 应返回副本")
	}
	// FromBytes 复制输入。
	src := []byte{0x0F}
	dec := FromBytes(src)
	src[0] = 0xF0
	if dec[0] != 0x0F {
		t.Fatalf("FromBytes 应复制输入，得到 %#b", dec[0])
	}
	if !slices.Equal(dec.Bytes(), []byte{0x0F}) {
		t.Fatalf("Bytes/FromBytes 往返失败")
	}

	// 空输入。
	if Encode(nil) != nil || Encode([]float32{}) != nil {
		t.Fatalf("空输入应返回 nil")
	}
	if FromBytes(nil) != nil || Binary(nil).Bytes() != nil {
		t.Fatalf("空字节应返回 nil")
	}
}

// Test_F50_HammingKnown 用已知位模式验证逐字节汉明距离。
func Test_F50_HammingKnown(t *testing.T) {
	t.Parallel()

	if got := Hamming(Binary{0x00}, Binary{0xFF}); got != 8 {
		t.Fatalf("零 vs 全一 = %d，期望 8", got)
	}
	if got := Hamming(Binary{0b1011}, Binary{0b1001}); got != 1 {
		t.Fatalf("逐位距离 = %d，期望 1", got)
	}
	if got := Hamming(Binary{0xFF, 0x00}, Binary{0x00, 0xFF}); got != 16 {
		t.Fatalf("多字节距离 = %d，期望 16", got)
	}
	if got := Hamming(Binary{0xFF}, Binary{0x00, 0xFF}); got != 8 {
		t.Fatalf("长度不等按公共前缀 = %d，期望 8", got)
	}
	if got := Hamming(Binary{}, Binary{0xFF}); got != 0 {
		t.Fatalf("空向量距离 = %d，期望 0", got)
	}
}

// Test_F50_NormGroupAndEntryConsistency 验证范数、桶号与落库记录的一致性。
func Test_F50_NormGroupAndEntryConsistency(t *testing.T) {
	t.Parallel()

	if got := Norm([]float32{3, 4}); math.Abs(got-5) > 1e-9 {
		t.Fatalf("‖(3,4)‖ = %v，期望 5", got)
	}
	if got := Group(5, 2.0, 64); got != 10 {
		t.Fatalf("Group(5) = %d，期望 10", got)
	}
	if got := Group(5, 2.0, 0); got != 10 {
		t.Fatalf("n=0 应回填默认 64，得到 %d", got)
	}

	cfg := DefaultConfig()
	mv := make([]float32, 64)
	mv[0] = -1 // 第 0 位为 0，便于检测返回副本被改动。
	e := cfg.NewEntry(7, "hello", mv)
	if len(e.Vector) != 8 || e.Vector.Len() != 64 {
		t.Fatalf("64 维应编码为 8 字节，得到 %d", len(e.Vector))
	}
	if e.Group != Group(e.Norm, cfg.K, cfg.N) {
		t.Fatalf("NewEntry 的 group 与 norm 不一致：%+v", e)
	}

	idx := NewIndex()
	idx.Add(7, "hello", mv)
	got, ok := idx.Get(7)
	if !ok || got.Text != "hello" || got.Group != e.Group {
		t.Fatalf("Get = %+v, %v，期望与 NewEntry 一致", got, ok)
	}
	// 返回的向量是副本。
	got.Vector[0] = 0xFF
	again, _ := idx.Get(7)
	if again.Vector[0] == 0xFF {
		t.Fatalf("Get 应返回向量副本")
	}

	// AddEntry 会依据 norm 纠正落库时的 group。
	idx.AddEntry(Entry{ID: 8, Text: "x", Vector: Encode(make([]float32, 64)), Norm: 5, Group: 999})
	e8, _ := idx.Get(8)
	if e8.Group != Group(5, cfg.K, cfg.N) {
		t.Fatalf("AddEntry 后 group = %d，期望按 norm 推导", e8.Group)
	}
}

// Test_F50_SearchTopKMatchesBruteForce 用 1000 条已知向量验证桶内 Top-5 与暴力一致。
func Test_F50_SearchTopKMatchesBruteForce(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.MaxDistance = 8
	idx := NewIndexWithConfig(cfg)
	rng := rand.New(rand.NewSource(2026))

	q := f50ScaledSign(rng, f50Dim, f50NormFor(10))
	qb := EncodeThreshold(q, cfg.VTB)
	if qg := Group(Norm(q), cfg.K, cfg.N); qg != 10 {
		t.Fatalf("查询桶 = %d，期望 10", qg)
	}

	type rec struct {
		id   int64
		text string
		v    []float32
		b    Binary
	}
	const n = 1000
	recs := make([]rec, 0, n)
	for i := 0; i < n; i++ {
		base := slices.Clone(q)
		for j := 0; j < i%9; j++ {
			p := (i*13 + j*5) % f50Dim
			base[p] = -base[p]
		}
		// 让记录落在查询桶及其左右相邻桶，保证半径 1 覆盖全部候选。
		v := f50ScaleTo(base, f50NormFor(10+(i%3)-1))
		recs = append(recs, rec{id: int64(i), text: "doc-" + strconv.Itoa(i), v: v, b: EncodeThreshold(v, cfg.VTB)})
	}

	// 打乱插入顺序。
	for _, k := range rng.Perm(n) {
		idx.Add(recs[k].id, recs[k].text, recs[k].v)
	}
	if idx.Len() != n {
		t.Fatalf("Len = %d，期望 %d", idx.Len(), n)
	}

	got, st := idx.SearchWithStats(q, 5)
	if st.Candidates != n {
		t.Fatalf("候选数 = %d，期望 %d（相邻桶应覆盖全部）", st.Candidates, n)
	}

	all := make([]Hit, 0, n)
	for _, r := range recs {
		if d := Hamming(qb, r.b); d <= cfg.MaxDistance {
			all = append(all, Hit{ID: r.id, Text: r.text, Distance: d})
		}
	}
	f50SortHits(all)
	want := all[:5]
	if !slices.Equal(got, want) {
		t.Fatalf("桶检索 Top-5 = %+v，暴力 = %+v", got, want)
	}
}

// Test_F50_SearchOrderAndTextID 验证距离单调不减、text 与 ID 一一对应，且打乱插入仍成立。
func Test_F50_SearchOrderAndTextID(t *testing.T) {
	t.Parallel()

	docs := map[int64]string{
		1: "a", 2: "b", 3: "c", 4: "d",
	}
	vecs := map[int64][]float32{
		1: {1, -1, 1, 1},    // 距离 1
		2: {1, 1, 1, -1},    // 距离 1
		3: {1, 1, -1, -1},   // 距离 2
		4: {-1, -1, -1, -1}, // 距离 4
	}

	idx := NewIndex()
	// 故意乱序插入。
	for _, id := range []int64{4, 2, 3, 1} {
		idx.Add(id, docs[id], vecs[id])
	}

	q := []float32{1, 1, 1, 1}
	got := idx.Search(q, 10)
	want := []Hit{
		{ID: 1, Text: "a", Distance: 1},
		{ID: 2, Text: "b", Distance: 1},
		{ID: 3, Text: "c", Distance: 2},
		{ID: 4, Text: "d", Distance: 4},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("结果 = %+v，期望 %+v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Distance > got[i].Distance {
			t.Fatalf("距离非升序：%+v", got)
		}
	}
	for _, h := range got {
		if docs[h.ID] != h.Text {
			t.Fatalf("ID %d 挂到了 %q，期望 %q", h.ID, h.Text, docs[h.ID])
		}
	}
}

// Test_F50_EmptyIndexSearch 验证空库查询返回空结果且不报错。
func Test_F50_EmptyIndexSearch(t *testing.T) {
	t.Parallel()

	q := []float32{1, -1, 1, -1}
	for _, idx := range []*Index{NewIndex(), new(Index)} {
		if got := idx.Search(q, 5); len(got) != 0 {
			t.Fatalf("空库 Search = %+v，期望空", got)
		}
		got, st := idx.SearchWithStats(q, 5)
		if len(got) != 0 || st.Candidates != 0 {
			t.Fatalf("空库 SearchWithStats = %+v / %+v，期望空", got, st)
		}
		if got := idx.SearchRadius(q, 5, 100); len(got) != 0 {
			t.Fatalf("空库 SearchRadius = %+v，期望空", got)
		}
		if idx.Search(q, 0) != nil || idx.Search(q, -1) != nil {
			t.Fatalf("k<=0 应返回 nil")
		}
		if idx.Search(nil, 5) != nil || idx.Search([]float32{}, 5) != nil {
			t.Fatalf("空查询应返回 nil")
		}
		if idx.Remove(1) {
			t.Fatalf("空库 Remove 应返回 false")
		}
		if _, ok := idx.Get(1); ok {
			t.Fatalf("空库 Get 应返回 false")
		}
	}
}

// Test_F50_BucketingReducesCandidates 验证分桶确实减少比较次数。
func Test_F50_BucketingReducesCandidates(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	idx := NewIndexWithConfig(cfg)
	const n = 1000
	v := make([]float32, f50Dim)
	for i := 0; i < n; i++ {
		v[0] = 1
		idx.Add(int64(i), "doc-"+strconv.Itoa(i), f50ScaleTo(v, f50NormFor(i%64)))
	}

	q := f50ScaledSign(rand.New(rand.NewSource(7)), f50Dim, f50NormFor(10))
	_, st := idx.SearchWithStats(q, 10)
	if st.Buckets != 3 {
		t.Fatalf("默认半径应扫描 3 个桶，得到 %d", st.Buckets)
	}
	if st.Candidates == 0 || st.Candidates*10 >= idx.Len() {
		t.Fatalf("候选数 = %d，未显著小于 N=%d", st.Candidates, idx.Len())
	}

	// 仅同桶：候选更少。
	_, one := idx.search(q, 10, 0)
	if one.Buckets != 1 || one.Candidates >= st.Candidates {
		t.Fatalf("半径 0 候选 = %d，应少于半径 1 的 %d", one.Candidates, st.Candidates)
	}

	// 半径足够大时退化为全量扫描。
	_, all := idx.search(q, 10, 64)
	if all.Candidates != n {
		t.Fatalf("大半径候选 = %d，期望 %d", all.Candidates, n)
	}
}

// Test_F50_RemoveAndOverwrite 验证覆盖与删除时桶索引保持一致。
func Test_F50_RemoveAndOverwrite(t *testing.T) {
	t.Parallel()

	// 距离阈值放宽，本用例只验证桶索引的一致性。
	idx := NewIndexWithConfig(Config{MaxDistance: f50Dim})
	a := make([]float32, f50Dim)
	a[0] = 1
	idx.Add(1, "one", f50ScaleTo(a, f50NormFor(10)))
	idx.Add(2, "two", f50ScaleTo(a, f50NormFor(20)))

	q10 := f50ScaledSign(rand.New(rand.NewSource(1)), f50Dim, f50NormFor(10))
	got := idx.SearchRadius(q10, 5, 0)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("桶 10 结果 = %+v，期望仅 id=1", got)
	}

	// 覆盖 id=1 到桶 20：旧桶必须移除，Len 不变。
	idx.Add(1, "one", f50ScaleTo(a, f50NormFor(20)))
	if idx.Len() != 2 {
		t.Fatalf("覆盖后 Len = %d，期望 2", idx.Len())
	}
	if got := idx.SearchRadius(q10, 5, 0); len(got) != 0 {
		t.Fatalf("覆盖后桶 10 应为空，得到 %+v", got)
	}
	if e, _ := idx.Get(1); e.Group != Group(e.Norm, 2.0, 64) {
		t.Fatalf("覆盖后 group 与 norm 不一致：%+v", e)
	}

	if !idx.Remove(2) {
		t.Fatalf("Remove 已存在 id 应返回 true")
	}
	if idx.Remove(2) {
		t.Fatalf("重复 Remove 应返回 false")
	}
	if idx.Len() != 1 {
		t.Fatalf("删除后 Len = %d，期望 1", idx.Len())
	}
}

// Test_F50_ConcurrentAddSearch 让并发读写暴露数据竞争（配合 -race 运行）。
func Test_F50_ConcurrentAddSearch(t *testing.T) {
	t.Parallel()

	idx := NewIndex()
	q := f50ScaledSign(rand.New(rand.NewSource(3)), f50Dim, f50NormFor(10))
	const workers, per = 8, 64

	var wg sync.WaitGroup
	var bad atomic.Int64
	v := make([]float32, f50Dim)
	v[0] = 1

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				idx.Add(int64(w*per+i), "t", f50ScaleTo(v, f50NormFor((w+i)%64)))
			}
		}(w)
	}
	for r := 0; r < workers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				res := idx.Search(q, 3)
				for j := 1; j < len(res); j++ {
					if res[j-1].Distance > res[j].Distance {
						bad.Add(1)
					}
				}
			}
		}()
	}
	wg.Wait()

	if bad.Load() != 0 {
		t.Fatalf("并发检索出现 %d 次乱序", bad.Load())
	}
	if idx.Len() != workers*per {
		t.Fatalf("并发写后 Len = %d，期望 %d", idx.Len(), workers*per)
	}
}

// BenchmarkHammingSearch 用 1 万个 256 位向量做检索基准（F-50 验收规模）。
// 向量均匀散布到 64 个桶，基准反映的是“同桶 + 相邻桶”路径。
func BenchmarkHammingSearch(b *testing.B) {
	const (
		n   = 10000
		dim = 256
	)
	rng := rand.New(rand.NewSource(1))
	// 距离阈值放宽到维度，确保候选都进入 Top-10 竞争。
	idx := NewIndexWithConfig(Config{MaxDistance: dim})
	for i := 0; i < n; i++ {
		idx.Add(int64(i), "doc", f50ScaledSign(rng, dim, f50NormFor(i%64)))
	}
	q := f50ScaledSign(rng, dim, f50NormFor(10))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if hits := idx.Search(q, 10); len(hits) != 10 {
			b.Fatalf("Top-10 应返回 10 条，得到 %d", len(hits))
		}
	}
}
