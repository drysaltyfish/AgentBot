package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Labels 是一个标签组合。标签名由注册时的 labelNames 固定，未提供的按空串处理。
type Labels map[string]string

type metricType string

const (
	typeCounter   metricType = "counter"
	typeGauge     metricType = "gauge"
	typeHistogram metricType = "histogram"
)

// series 是一个标签组合下的取值。
//
// 计数器/仪表用原子变量存 float64 的位模式：抓取与递增并发，用锁会把热路径串行化。
// 直方图有多个字段要一起更新，只能用锁。
type series struct {
	values []string
	bits   atomic.Uint64

	hmu          sync.Mutex
	bucketCounts []uint64
	sum          float64
	count        uint64
}

func (s *series) add(v float64) {
	for {
		old := s.bits.Load()
		nv := math.Float64frombits(old) + v
		if s.bits.CompareAndSwap(old, math.Float64bits(nv)) {
			return
		}
	}
}

func (s *series) set(v float64)  { s.bits.Store(math.Float64bits(v)) }
func (s *series) value() float64 { return math.Float64frombits(s.bits.Load()) }

// family 是一种指标的全部序列（同一个 name/help/type/标签名集合）。
type family struct {
	name       string
	help       string
	typ        metricType
	labelNames []string
	buckets    []float64

	mu     sync.Mutex
	series map[string]*series
}

// child 取（或惰性建）某个标签组合的序列。
func (f *family) child(l Labels) *series {
	values := make([]string, len(f.labelNames))
	for i, n := range f.labelNames {
		values[i] = l[n]
	}
	key := strings.Join(values, "\x00")

	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.series[key]
	if !ok {
		s = &series{values: values}
		if f.typ == typeHistogram {
			s.bucketCounts = make([]uint64, len(f.buckets))
		}
		f.series[key] = s
	}
	return s
}

// Registry 持有本进程的全部指标。
//
// 刻意不做成包级全局：全局可变状态会让测试互相污染，也无法在一个进程里跑多实例
// （FEATURES.md 反模式 #1）。一切经实例传递。
type Registry struct {
	mu         sync.Mutex
	families   map[string]*family
	gaugeFuncs map[string]*GaugeFunc
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{families: map[string]*family{}, gaugeFuncs: map[string]*GaugeFunc{}}
}

func (r *Registry) register(name, help string, typ metricType, buckets []float64, labelNames []string) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		return f
	}
	f := &family{name: name, help: help, typ: typ, labelNames: labelNames, buckets: buckets, series: map[string]*series{}}
	r.families[name] = f
	return f
}

// Counter 是只增不减的累计值。
type Counter struct {
	fam    *family
	labels Labels
}

// Counter 注册（或复用）一个计数器。
func (r *Registry) Counter(name, help string, labelNames ...string) *Counter {
	return &Counter{fam: r.register(name, help, typeCounter, nil, labelNames)}
}

// Inc 自增 1。
func (c *Counter) Inc() { c.Add(1) }

// Add 累加 v。
func (c *Counter) Add(v float64) { c.fam.child(c.labels).add(v) }

// With 绑定一组标签，返回同一指标族的子指标。
func (c *Counter) With(l Labels) *Counter { return &Counter{fam: c.fam, labels: l} }

// Gauge 是可增可减的瞬时值。
type Gauge struct {
	fam    *family
	labels Labels
}

// Gauge 注册（或复用）一个仪表。
func (r *Registry) Gauge(name, help string, labelNames ...string) *Gauge {
	return &Gauge{fam: r.register(name, help, typeGauge, nil, labelNames)}
}

// Set 设置当前值。
func (g *Gauge) Set(v float64) { g.fam.child(g.labels).set(v) }

// With 绑定一组标签。
func (g *Gauge) With(l Labels) *Gauge { return &Gauge{fam: g.fam, labels: l} }

// GaugeFunc 在抓取时求值，用于"本来就在别处维护"的量（活跃会话数、队列深度）。
type GaugeFunc struct {
	name string
	help string
	fn   func() float64
}

// GaugeFunc 注册一个回调式仪表。
func (r *Registry) GaugeFunc(name, help string, fn func() float64) *GaugeFunc {
	if fn == nil {
		fn = func() float64 { return 0 }
	}
	g := &GaugeFunc{name: name, help: help, fn: fn}
	r.mu.Lock()
	r.gaugeFuncs[name] = g
	r.mu.Unlock()
	return g
}

// Histogram 是分桶直方图。
type Histogram struct {
	fam    *family
	labels Labels
}

// Histogram 注册（或复用）一个直方图；buckets 会拷贝并排序，调用方之后改原切片无影响。
func (r *Registry) Histogram(name, help string, buckets []float64, labelNames ...string) *Histogram {
	bs := append([]float64(nil), buckets...)
	sort.Float64s(bs)
	return &Histogram{fam: r.register(name, help, typeHistogram, bs, labelNames)}
}

// Observe 记录一次观测。
func (h *Histogram) Observe(v float64) {
	s := h.fam.child(h.labels)
	s.hmu.Lock()
	// 只落进**第一个**命中的桶：bucketCounts 存的是非累计计数，
	// 输出时再累加成 Prometheus 要求的 le 语义。两者混用会重复计数。
	for i, b := range h.fam.buckets {
		if v <= b {
			s.bucketCounts[i]++
			break
		}
	}
	s.sum += v
	s.count++
	s.hmu.Unlock()
}

// With 绑定一组标签。
func (h *Histogram) With(l Labels) *Histogram { return &Histogram{fam: h.fam, labels: l} }

// WritePrometheus 以 Prometheus 文本格式写出全部指标族与回调仪表。
//
// 输出顺序固定（族名与序列键均排序），因此同样内容两次抓取逐字节相同，便于回归。
func (r *Registry) WritePrometheus(w io.Writer) error {
	r.mu.Lock()
	fams := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		fams = append(fams, f)
	}
	funcs := make([]*GaugeFunc, 0, len(r.gaugeFuncs))
	for _, g := range r.gaugeFuncs {
		funcs = append(funcs, g)
	}
	r.mu.Unlock()

	sort.Slice(fams, func(i, j int) bool { return fams[i].name < fams[j].name })
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].name < funcs[j].name })

	var b strings.Builder
	for _, f := range fams {
		writeFamily(&b, f)
	}
	for _, g := range funcs {
		fmt.Fprintf(&b, "# HELP %s %s\n", g.name, escapeHelp(g.help))
		fmt.Fprintf(&b, "# TYPE %s gauge\n", g.name)
		fmt.Fprintf(&b, "%s %s\n", g.name, formatFloat(g.fn()))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeFamily 写出单个指标族的 HELP/TYPE 与全部序列。
func writeFamily(b *strings.Builder, f *family) {
	fmt.Fprintf(b, "# HELP %s %s\n", f.name, escapeHelp(f.help))
	fmt.Fprintf(b, "# TYPE %s %s\n", f.name, f.typ)

	f.mu.Lock()
	keys := make([]string, 0, len(f.series))
	for k := range f.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]*series, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, f.series[k])
	}
	f.mu.Unlock()

	for _, s := range ordered {
		if f.typ == typeHistogram {
			writeHistogram(b, f, s)
			continue
		}
		labels := renderLabels(f.labelNames, s.values)
		if labels == "" {
			fmt.Fprintf(b, "%s %s\n", f.name, formatFloat(s.value()))
			continue
		}
		fmt.Fprintf(b, "%s{%s} %s\n", f.name, labels, formatFloat(s.value()))
	}
}

// writeHistogram 写出直方图的 _bucket/_sum/_count 样本。
func writeHistogram(b *strings.Builder, f *family, s *series) {
	s.hmu.Lock()
	counts := append([]uint64(nil), s.bucketCounts...)
	sum, count := s.sum, s.count
	s.hmu.Unlock()

	var cumulative uint64
	for i, bound := range f.buckets {
		if i < len(counts) {
			cumulative += counts[i]
		}
		labels := appendLabel(renderLabels(f.labelNames, s.values), "le", formatFloat(bound))
		fmt.Fprintf(b, "%s_bucket{%s} %d\n", f.name, labels, cumulative)
	}
	infLabels := appendLabel(renderLabels(f.labelNames, s.values), "le", "+Inf")
	fmt.Fprintf(b, "%s_bucket{%s} %d\n", f.name, infLabels, count)

	base := renderLabels(f.labelNames, s.values)
	if base != "" {
		base = "{" + base + "}"
	}
	fmt.Fprintf(b, "%s_sum%s %s\n", f.name, base, formatFloat(sum))
	fmt.Fprintf(b, "%s_count%s %d\n", f.name, base, count)
}

// appendLabel 在已渲染标签串后追加一个标签，自动处理逗号。
func appendLabel(rendered, name, value string) string {
	pair := name + "=" + "\"" + escapeLabel(value) + "\""
	if rendered == "" {
		return pair
	}
	return rendered + "," + pair
}

// renderLabels 按标签名排序渲染标签串；缺少的标签值按空串处理。
func renderLabels(names, vals []string) string {
	type kv struct{ name, value string }
	pairs := make([]kv, 0, len(names))
	for i, n := range names {
		v := ""
		if i < len(vals) {
			v = vals[i]
		}
		pairs = append(pairs, kv{name: n, value: v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.name+"="+"\""+escapeLabel(p.value)+"\"")
	}
	return strings.Join(parts, ",")
}

// escapeLabel 按 Prometheus 文本格式转义标签值：反斜杠、双引号、换行。
func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	v = strings.ReplaceAll(v, "\n", "\\n")
	return v
}

// escapeHelp 转义 HELP 文本。
func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	return v
}

// formatFloat 渲染 Prometheus 可解析的浮点值。
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}
