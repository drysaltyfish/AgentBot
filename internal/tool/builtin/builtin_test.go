package builtin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

func newRegistry(t *testing.T, deps Deps) *tool.Registry {
	t.Helper()
	r := tool.New()
	if err := Register(r, deps); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r
}

func run(t *testing.T, r *tool.Registry, name, args string) tool.Result {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("工具 %s 未注册", name)
	}
	res, err := tl.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s 不应返回 error（要回灌）: %v", name, err)
	}
	return res
}

func Test_F44_RegisterIsOrderStable(t *testing.T) {
	t.Parallel()
	r1 := newRegistry(t, Deps{})
	r2 := newRegistry(t, Deps{})
	if strings.Join(r1.Names(), ",") != strings.Join(r2.Names(), ",") {
		t.Fatalf("注册顺序不稳定: %v vs %v", r1.Names(), r2.Names())
	}
	want := "calculator,current_time,json_query,http_fetch,memory_save,memory_recall,recall_history,forget_memory,list_memories"
	if got := strings.Join(r1.Names(), ","); got != want {
		t.Fatalf("内置工具集顺序: actual=%q expected=%q", got, want)
	}
}

// Test_F44_CalculatorRejectsInjection 是验收点：恶意/畸形输入必须返回错误而不是 panic。
func Test_F44_CalculatorRejectsInjection(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	for _, bad := range []string{
		`{"expr":"__import__(\"os\")"}`,
		`{"expr":"1+"}`,
		`{"expr":"((1+2)*3"}`,
		`{"expr":"open(\"/etc/passwd\")"}`,
		`{"expr":"1/0"}`,
		`{"expr":"2.5%1"}`,
		`{"expr":"1e300*1e300"}`,
	} {
		res := run(t, r, "calculator", bad)
		if !res.Failed() {
			t.Fatalf("非法表达式应失败: %s -> %+v", bad, res)
		}
	}
}

func Test_F44_CalculatorEvaluatesValidExpressions(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	cases := []struct{ in, want string }{
		{`{"expr":"1+2*3"}`, "7"},
		{`{"expr":"(1+2)*3"}`, "9"},
		{`{"expr":"-4+10"}`, "6"},
		{`{"expr":"10%3"}`, "1"},
		{`{"expr":"7/2"}`, "3.5"},
	}
	for _, tc := range cases {
		res := run(t, r, "calculator", tc.in)
		if res.Failed() {
			t.Fatalf("%s 应成功: %s", tc.in, res.Error)
		}
		if strings.TrimSpace(res.Output) != tc.want {
			t.Fatalf("%s: actual=%q expected=%q", tc.in, res.Output, tc.want)
		}
	}
}

// Test_F44_HTTPFetchRejectsPrivateAddresses 是验收点。
func Test_F44_HTTPFetchRejectsPrivateAddresses(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{HTTP: httpx.Defaults()})
	for _, target := range []string{
		`{"url":"http://127.0.0.1/"}`,
		`{"url":"http://169.254.169.254/latest/meta-data/"}`,
		`{"url":"http://10.0.0.1/"}`,
		`{"url":"http://[::1]/"}`,
		`{"url":"file:///etc/passwd"}`,
	} {
		res := run(t, r, "http_fetch", target)
		if !res.Failed() {
			t.Fatalf("私网/非 http 目标必须被拒绝: %s -> %+v", target, res)
		}
	}
}

func Test_F44_HTTPFetchRejectsBadArgs(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	res := run(t, r, "http_fetch", `{}`)
	if !res.Failed() || !strings.Contains(res.Error, "url") {
		t.Fatalf("缺 url 应给出可纠正的错误: %+v", res)
	}
}

func Test_F44_CurrentTimeAndTimezoneValidation(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	r := newRegistry(t, Deps{Now: func() time.Time { return fixed }})

	res := run(t, r, "current_time", `{}`)
	if res.Failed() || !strings.HasPrefix(res.Output, "2026-10-03T05:00:00Z") {
		t.Fatalf("默认 UTC: %+v", res)
	}

	res = run(t, r, "current_time", `{"timezone":"Asia/Shanghai"}`)
	if res.Failed() || !strings.HasPrefix(res.Output, "2026-10-03T13:00:00+08:00") {
		t.Fatalf("时区换算: %+v", res)
	}

	res = run(t, r, "current_time", `{"timezone":"Not/AZone"}`)
	if !res.Failed() {
		t.Fatalf("非法时区应失败: %+v", res)
	}

	res = run(t, r, "current_time", `{"format":"unix"}`)
	// 期望值由同一个时间源推导，避免把时间戳硬编码错（上一版就是猜错了）。
	if want := strconv.FormatInt(fixed.Unix(), 10); res.Failed() || res.Output != want {
		t.Fatalf("unix 格式: actual=%+v expected=%s", res, want)
	}
}

// Test_F44_JSONQueryKeepsIntegerPrecision 守住 F-39 的同类教训。
func Test_F44_JSONQueryKeepsIntegerPrecision(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	in := `{"json":"{\"id\":1234567890123456789,\"a\":{\"b\":[1,2]}}","path":"id"}`
	res := run(t, r, "json_query", in)
	if res.Failed() {
		t.Fatalf("查询失败: %+v", res)
	}
	if strings.TrimSpace(res.Output) != "1234567890123456789" {
		t.Fatalf("大整数精度丢失: %q", res.Output)
	}

	in = `{"json":"{\"a\":{\"b\":[1,2]}}","path":"a.b[1]"}`
	if res := run(t, r, "json_query", in); res.Failed() || strings.TrimSpace(res.Output) != "2" {
		t.Fatalf("嵌套路径: %+v", res)
	}

	for _, path := range []string{"nope", "a.b[9]", "a.b.c"} {
		in := `{"json":"{\"a\":{\"b\":[1,2]}}","path":"` + path + `"}`
		if res := run(t, r, "json_query", in); !res.Failed() {
			t.Fatalf("非法路径 %s 应失败: %+v", path, res)
		}
	}
}

// memStore 是测试用记忆实现。
type memStore struct{ items []string }

func (m *memStore) Save(ctx context.Context, text string) error {
	m.items = append(m.items, text)
	return nil
}
func (m *memStore) Recall(ctx context.Context) ([]string, error) { return m.items, nil }

func Test_F44_MemoryToolsValidateAndTruncate(t *testing.T) {
	t.Parallel()
	mem := &memStore{}
	r := newRegistry(t, Deps{Memory: mem})

	if res := run(t, r, "memory_save", `{"text":"主人喜欢橘子味"}`); res.Failed() {
		t.Fatalf("合法记忆应成功: %+v", res)
	}
	if len(mem.items) != 1 {
		t.Fatalf("记忆未写入: %v", mem.items)
	}

	for _, bad := range []string{
		`{"text":"   "}`,
		`{"text":"第一行` + "\\n" + `第二行"}`,
	} {
		if res := run(t, r, "memory_save", bad); !res.Failed() {
			t.Fatalf("非法记忆应失败: %s -> %+v", bad, res)
		}
	}

	if res := run(t, r, "memory_recall", `{}`); res.Failed() || !strings.Contains(res.Output, "橘子") {
		t.Fatalf("recall: %+v", res)
	}

	// 未配置记忆时必须明确失败，而不是假装成功。
	bare := newRegistry(t, Deps{})
	if res := run(t, bare, "memory_save", `{"text":"x"}`); !res.Failed() {
		t.Fatalf("未配置记忆应明确失败: %+v", res)
	}
	if res := run(t, bare, "memory_recall", `{}`); !res.Failed() {
		t.Fatalf("未配置记忆应明确失败: %+v", res)
	}
}

func Test_F44_TruncateOutputMarked(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("字", MaxOutput+100)
	got := truncateOutput(long)
	if !strings.HasSuffix(got, "[truncated]") {
		t.Fatalf("超长输出必须有截断标注")
	}
	if len([]rune(got)) > MaxOutput+len([]rune("\n[truncated]")) {
		t.Fatalf("截断后仍然过长")
	}
	if truncateOutput("短") != "短" {
		t.Fatalf("短输出不应被改动")
	}
}

func Test_F44_QueryJSONErrorsAreStructured(t *testing.T) {
	t.Parallel()
	if _, err := QueryJSON([]byte("not json"), "a"); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	if _, err := QueryJSON([]byte(`{"a":1}`), ""); err != nil {
		t.Fatalf("空路径应返回整个文档: %v", err)
	}
	if _, err := QueryJSON([]byte(`{"a":1}`), "a.b"); err == nil {
		t.Fatalf("在标量上继续取键应报错")
	}
}

// memHistory 是测试用的内存历史（实现 builtin.HistoryReader）。
type memHistory struct{ byKey map[string][]history.Item }

func newMemHistory() *memHistory { return &memHistory{byKey: map[string][]history.Item{}} }
func (m *memHistory) Messages(ctx context.Context, key string) ([]history.Item, error) {
	return m.byKey[key], nil
}

func Test_F38_RecallHistoryReadsCurrentSession(t *testing.T) {
	t.Parallel()
	h := newMemHistory()
	h.byKey["k1"] = []history.Item{
		{Kind: history.KindUser, Content: "我喜欢橘子"},
		{Kind: history.KindAssistant, Content: "记住啦"},
		{Kind: history.KindMarker, Content: "内部记忆条目"},
		{Kind: history.KindUser, Content: "今天天气不错"},
	}
	h.byKey["k2"] = []history.Item{{Kind: history.KindUser, Content: "别的会话的内容"}}

	r := newRegistry(t, Deps{History: h})
	ctx := tool.WithScope(context.Background(), "k1")

	res := execWith(t, r, ctx, "recall_history", `{"query":"橘子"}`)
	if res.Failed() {
		t.Fatalf("召回失败: %+v", res)
	}
	if !strings.Contains(res.Output, "我喜欢橘子") {
		t.Fatalf("应命中相关历史: %q", res.Output)
	}
	if strings.Contains(res.Output, "别的会话的内容") {
		t.Fatalf("绝不能召回其它会话的历史: %q", res.Output)
	}
	if strings.Contains(res.Output, "内部记忆条目") {
		t.Fatalf("marker 不是对话，不应参与召回: %q", res.Output)
	}
	if strings.Contains(res.Output, "今天天气不错") {
		t.Fatalf("不匹配关键词的不应出现: %q", res.Output)
	}
}

func Test_F38_RecallHistoryWithoutScopeFailsLoudly(t *testing.T) {
	t.Parallel()
	h := newMemHistory()
	r := newRegistry(t, Deps{History: h})
	// 没有作用域时**必须失败**，绝不能退化成"读全部历史"。
	res := execWith(t, r, context.Background(), "recall_history", `{}`)
	if !res.Failed() {
		t.Fatalf("无作用域应明确失败: %+v", res)
	}
	if !strings.Contains(res.Error, "会话") {
		t.Fatalf("错误信息应说明原因: %q", res.Error)
	}
}

func Test_F38_RecallHistoryLimitAndEmpty(t *testing.T) {
	t.Parallel()
	h := newMemHistory()
	for i := 0; i < 30; i++ {
		h.byKey["k"] = append(h.byKey["k"], history.Item{Kind: history.KindUser, Content: "第" + strconv.Itoa(i) + "条"})
	}
	r := newRegistry(t, Deps{History: h})
	ctx := tool.WithScope(context.Background(), "k")

	res := execWith(t, r, ctx, "recall_history", `{"limit":3}`)
	if res.Failed() {
		t.Fatalf("召回失败: %+v", res)
	}
	if n := strings.Count(res.Output, "条"); n != 3 {
		t.Fatalf("应限制为 3 条，实际 %d: %q", n, res.Output)
	}
	// 超出硬上限应被夹到 MaxRecallLimit。
	res = execWith(t, r, ctx, "recall_history", `{"limit":9999}`)
	if res.Failed() {
		t.Fatalf("召回失败: %+v", res)
	}
	if n := strings.Count(res.Output, "条"); n != 30 {
		t.Fatalf("硬上限内的全部历史应返回，实际 %d", n)
	}

	empty := newMemHistory()
	r2 := newRegistry(t, Deps{History: empty})
	res = execWith(t, r2, tool.WithScope(context.Background(), "none"), "recall_history", `{}`)
	if res.Failed() || !strings.Contains(res.Output, "还没有历史") {
		t.Fatalf("空历史应给出可读提示: %+v", res)
	}
}

func Test_F38_RecallHistoryWithoutStoreFailsLoudly(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	res := execWith(t, r, tool.WithScope(context.Background(), "k"), "recall_history", `{}`)
	if !res.Failed() || !strings.Contains(res.Error, "历史存储") {
		t.Fatalf("未配置历史存储应明确失败: %+v", res)
	}
}

func execWith(t *testing.T, r *tool.Registry, ctx context.Context, name, args string) tool.Result {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("工具 %s 未注册", name)
	}
	res, err := tl.Execute(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s 不应返回 error（要回灌）: %v", name, err)
	}
	return res
}

// searchableHistory 额外实现 HistorySearcher，用于验证 recall_history 优先走检索。
type searchableHistory struct {
	memHistory
	searched  int
	lastQuery string
	hits      []history.Hit
}

func (s *searchableHistory) Search(ctx context.Context, key, query string, limit int) ([]history.Hit, error) {
	s.searched++
	s.lastQuery = query
	if key != "k1" {
		return nil, nil
	}
	return s.hits, nil
}

// Test_F84_RecallHistoryPrefersSearch 守住"有检索能力时走检索"。
func Test_F84_RecallHistoryPrefersSearch(t *testing.T) {
	t.Parallel()
	sh := &searchableHistory{hits: []history.Hit{{
		Item:    history.Item{Kind: history.KindUser, Content: "我喜欢喝橙汁", At: time.Now()},
		Snippet: "我喜欢[喝橙汁]",
		Before:  &history.Item{Kind: history.KindAssistant, Content: "记住啦"},
	}}}
	r := newRegistry(t, Deps{History: sh})
	ctx := tool.WithScope(context.Background(), "k1")

	res := execWith(t, r, ctx, "recall_history", `{"query":"橙汁"}`)
	if res.Failed() {
		t.Fatalf("召回失败: %+v", res)
	}
	if sh.searched != 1 {
		t.Fatalf("应走检索路径，实际调用 %d 次", sh.searched)
	}
	if sh.lastQuery != "橙汁" {
		t.Fatalf("关键词未透传: %q", sh.lastQuery)
	}
	if !strings.Contains(res.Output, "我喜欢喝橙汁") {
		t.Fatalf("应包含命中内容: %q", res.Output)
	}
	if !strings.Contains(res.Output, "片段") || !strings.Contains(res.Output, "[") {
		t.Fatalf("应带片段: %q", res.Output)
	}
	if !strings.Contains(res.Output, "上文") {
		t.Fatalf("应带上文: %q", res.Output)
	}
}

// Test_F84_RecallHistorySearchNoHits 覆盖检索无结果的可读提示。
func Test_F84_RecallHistorySearchNoHits(t *testing.T) {
	t.Parallel()
	sh := &searchableHistory{}
	r := newRegistry(t, Deps{History: sh})
	res := execWith(t, r, tool.WithScope(context.Background(), "k1"), "recall_history", `{"query":"不存在"}`)
	if res.Failed() {
		t.Fatalf("无结果不该报错: %+v", res)
	}
	if !strings.Contains(res.Output, "没有找到") {
		t.Fatalf("应给出可读提示: %q", res.Output)
	}
}

// adminMemory 实现 MemoryAdmin，用于测试遗忘与检视工具。
type adminMemory struct {
	items      []store.Memory
	forgot     []int64
	scopeWiped bool
}

func (a *adminMemory) Forget(ctx context.Context, id int64) (bool, error) {
	a.forgot = append(a.forgot, id)
	for i, it := range a.items {
		if it.ID == id {
			a.items = append(a.items[:i], a.items[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (a *adminMemory) ForgetScope(ctx context.Context) (int, error) {
	a.scopeWiped = true
	n := len(a.items)
	a.items = nil
	return n, nil
}

func (a *adminMemory) List(ctx context.Context, limit int) ([]store.Memory, error) {
	if limit < len(a.items) {
		return a.items[:limit], nil
	}
	return a.items, nil
}

func Test_F88_ForgetMemoryTool(t *testing.T) {
	t.Parallel()
	am := &adminMemory{items: []store.Memory{{ID: 7, Text: "要被忘掉的事"}}}
	r := newRegistry(t, Deps{MemoryAdmin: am})

	res := execWith(t, r, context.Background(), "forget_memory", `{"id":7}`)
	if res.Failed() {
		t.Fatalf("遗忘失败: %+v", res)
	}
	if len(am.forgot) != 1 || am.forgot[0] != 7 {
		t.Fatalf("应调用 Forget(7): %v", am.forgot)
	}
	if !strings.Contains(res.Output, "已忘掉") {
		t.Fatalf("应给出可读确认: %q", res.Output)
	}

	// 幂等：删不存在的 id 不报错，但要说明没删到。
	res = execWith(t, r, context.Background(), "forget_memory", `{"id":999}`)
	if res.Failed() {
		t.Fatalf("删不存在的 id 不该报错: %+v", res)
	}
	if !strings.Contains(res.Output, "没有找到") {
		t.Fatalf("应说明没删到: %q", res.Output)
	}
}

func Test_F88_ForgetAllRequiresExplicitFlag(t *testing.T) {
	t.Parallel()
	am := &adminMemory{items: []store.Memory{{ID: 1, Text: "a"}, {ID: 2, Text: "b"}}}
	r := newRegistry(t, Deps{MemoryAdmin: am})

	// 没给 all、也没给 id -> 必须明确失败，不能默认清空。
	res := execWith(t, r, context.Background(), "forget_memory", `{}`)
	if !res.Failed() {
		t.Fatalf("既没 id 也没 all 时必须失败，不得默认清空: %+v", res)
	}
	if am.scopeWiped {
		t.Fatalf("不得清空全部记忆")
	}

	res = execWith(t, r, context.Background(), "forget_memory", `{"all":true}`)
	if res.Failed() || !am.scopeWiped {
		t.Fatalf("显式 all=true 才清空: %+v", res)
	}
}

func Test_F88_ListMemoriesTool(t *testing.T) {
	t.Parallel()
	am := &adminMemory{items: []store.Memory{
		{ID: 1, Text: "喜欢橘子"}, {ID: 2, Text: "住在杭州"}, {ID: 3, Text: "养了只猫"},
	}}
	r := newRegistry(t, Deps{MemoryAdmin: am})

	res := execWith(t, r, context.Background(), "list_memories", `{}`)
	if res.Failed() {
		t.Fatalf("列出失败: %+v", res)
	}
	for _, want := range []string{"#1 喜欢橘子", "#2 住在杭州", "#3 养了只猫"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("应列出 %q: %q", want, res.Output)
		}
	}

	// limit 生效。
	res = execWith(t, r, context.Background(), "list_memories", `{"limit":1}`)
	if strings.Contains(res.Output, "#2") {
		t.Fatalf("limit=1 时不该列出第二条: %q", res.Output)
	}
}

func Test_F88_MemoryAdminUnavailableFailsLoudly(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{})
	for _, name := range []string{"forget_memory", "list_memories"} {
		res := execWith(t, r, context.Background(), name, `{}`)
		if !res.Failed() {
			t.Fatalf("%s 在未配置时应明确失败: %+v", name, res)
		}
	}
}

func Test_F88_ListMemoriesEmpty(t *testing.T) {
	t.Parallel()
	r := newRegistry(t, Deps{MemoryAdmin: &adminMemory{}})
	res := execWith(t, r, context.Background(), "list_memories", `{}`)
	if res.Failed() || !strings.Contains(res.Output, "还没有记住") {
		t.Fatalf("空记忆应给出可读提示: %+v", res)
	}
}
