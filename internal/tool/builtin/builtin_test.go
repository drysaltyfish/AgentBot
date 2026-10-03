package builtin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/httpx"
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
	want := "calculator,current_time,json_query,http_fetch,memory_save,memory_recall"
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
