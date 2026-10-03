package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeTool 是测试用工具，可声明可选属性。
type fakeTool struct {
	name        string
	desc        string
	schema      Schema
	readOnly    bool
	concurrent  bool
	dangerous   bool
	declaresRO  bool
	declaresCS  bool
	declaresDGR bool
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return f.desc }
func (f *fakeTool) Parameters() Schema  { return f.schema }
func (f *fakeTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	return Success("ok"), nil
}
func (f *fakeTool) ReadOnly() bool        { return f.readOnly }
func (f *fakeTool) ConcurrencySafe() bool { return f.concurrent }
func (f *fakeTool) Dangerous() bool       { return f.dangerous }

// bareTool 只实现 Tool 的四个必需方法，不声明任何可选属性。
type bareTool struct{ name string }

func (b *bareTool) Name() string        { return b.name }
func (b *bareTool) Description() string { return "" }
func (b *bareTool) Parameters() Schema  { return Schema{} }
func (b *bareTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	return Success(""), nil
}

func newFake(name string) *fakeTool {
	return &fakeTool{name: name, desc: name + " 的描述", schema: Schema{
		Properties: map[string]Property{"q": {Type: "string", Description: "查询词"}},
		Required:   []string{"q"},
	}}
}

func Test_F41_DefinitionsAreByteStable(t *testing.T) {
	t.Parallel()
	r := New()
	for _, n := range []string{"alpha", "beta", "gamma"} {
		r.MustRegister(newFake(n))
	}

	first, err := json.Marshal(r.Definitions())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := json.Marshal(r.Definitions())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("Definitions 不是逐字节稳定的（第 %d 次）:\n%s\n%s", i+1, first, again)
		}
	}

	// 顺序必须等于注册顺序，否则工具段会抖动并破坏前缀缓存。
	names := r.Names()
	want := []string{"alpha", "beta", "gamma"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("注册顺序: actual=%v expected=%v", names, want)
	}
}

func Test_F41_SubsetIsIndependent(t *testing.T) {
	t.Parallel()
	parent := New()
	parent.MustRegister(newFake("a"))
	parent.MustRegister(newFake("b"))
	parent.MustRegister(newFake("c"))

	sub := parent.Subset("a", "c", "does-not-exist")
	if got := strings.Join(sub.Names(), ","); got != "a,c" {
		t.Fatalf("子集内容: actual=%q", got)
	}
	if parent.Len() != 3 {
		t.Fatalf("派生不应影响父注册表: %d", parent.Len())
	}

	// 往子集里加工具不能影响父表。
	sub.MustRegister(newFake("d"))
	if _, ok := parent.Get("d"); ok {
		t.Fatalf("子集注册泄漏到了父注册表")
	}
	// 往父表里加也不能影响已派生的子集。
	parent.MustRegister(newFake("e"))
	if _, ok := sub.Get("e"); ok {
		t.Fatalf("父注册表注册泄漏到了子集")
	}
}

func Test_F41_RejectsInvalidNameAndDuplicate(t *testing.T) {
	t.Parallel()
	r := New()
	for _, bad := range []string{"", "has space", "中文名", strings.Repeat("x", 65), "a/b"} {
		err := r.Register(newFake(bad))
		if !errors.Is(err, ErrInvalidName) {
			t.Fatalf("非法名 %q 应返回 ErrInvalidName，实际=%v", bad, err)
		}
	}
	if err := r.Register(nil); !errors.Is(err, ErrNilTool) {
		t.Fatalf("注册 nil 应返回 ErrNilTool，实际=%v", err)
	}
	r.MustRegister(newFake("dup"))
	if err := r.Register(newFake("dup")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("重名应返回 ErrDuplicate，实际=%v", err)
	}
}

func Test_F41_RemoveKeepsRemainingOrder(t *testing.T) {
	t.Parallel()
	r := New()
	for _, n := range []string{"a", "b", "c", "d"} {
		r.MustRegister(newFake(n))
	}
	if !r.Remove("b") {
		t.Fatalf("Remove 应返回 true")
	}
	if r.Remove("b") {
		t.Fatalf("重复 Remove 应返回 false")
	}
	if got := strings.Join(r.Names(), ","); got != "a,c,d" {
		t.Fatalf("移除后顺序: actual=%q", got)
	}
	// Remove 之后 Definitions 依然稳定。
	x, _ := json.Marshal(r.Definitions())
	y, _ := json.Marshal(r.Definitions())
	if string(x) != string(y) {
		t.Fatalf("Remove 后 Definitions 不稳定")
	}
}

func Test_F41_ConcurrentRegisterAndDefinitions(t *testing.T) {
	t.Parallel()
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(newFake(fmt.Sprintf("tool-%d", i)))
		}(i)
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = r.Definitions()
				_ = r.List()
				_ = r.Names()
			}
		}()
	}
	wg.Wait()
	if r.Len() != 16 {
		t.Fatalf("并发注册后数量: actual=%d expected=16", r.Len())
	}
}

func Test_F41_LongDescriptionIsTruncatedWithWarning(t *testing.T) {
	t.Parallel()
	var warnings []string
	r := New(WithWarnFunc(func(msg string) { warnings = append(warnings, msg) }))
	tool := newFake("verbose")
	tool.desc = strings.Repeat("很", MaxDescription+50)
	r.MustRegister(tool)

	defs := r.Definitions()
	if len(defs) != 1 {
		t.Fatalf("definitions: %d", len(defs))
	}
	if got := len([]rune(defs[0].Description)); got != MaxDescription {
		t.Fatalf("描述应被截断到 %d，实际 %d", MaxDescription, got)
	}
	if len(warnings) == 0 {
		t.Fatalf("截断必须告警")
	}
}

func Test_F41_SchemaEncodesToStableJSON(t *testing.T) {
	t.Parallel()
	min := 1.0
	s := Schema{
		Properties: map[string]Property{
			"b": {Type: "integer", Minimum: &min},
			"a": {Type: "string", Enum: []string{"x", "y"}, Description: "d"},
		},
		Required: []string{"a"},
	}
	first, err := s.JSONSchema()
	if err != nil {
		t.Fatalf("JSONSchema: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, _ := s.JSONSchema()
		if string(again) != string(first) {
			t.Fatalf("map 键序不稳定: %s vs %s", first, again)
		}
	}
	if !strings.Contains(string(first), `"type":"object"`) {
		t.Fatalf("默认应补成 object: %s", first)
	}
}

func Test_F42_ResultStringAndFailure(t *testing.T) {
	t.Parallel()
	if got := Success("done").String(); got != "done" {
		t.Fatalf("Success.String: %q", got)
	}
	fail := Failure("boom")
	if !fail.Failed() {
		t.Fatalf("Failure 应被判定为失败")
	}
	if got := fail.String(); got != "boom" {
		t.Fatalf("Failure.String 应返回错误文本: %q", got)
	}
}

// Test_F42_OptionalAttributesAreConservative 守住默认值方向：不确定时按更严格的一侧取值。
func Test_F42_OptionalAttributesAreConservative(t *testing.T) {
	t.Parallel()
	plain := &bareTool{name: "plain"}
	if IsReadOnly(plain) {
		t.Fatalf("未声明 ReadOnly 时应按 false（保守）")
	}
	if IsConcurrencySafe(plain) {
		t.Fatalf("未声明 ConcurrencySafe 时应按 false（保守）")
	}
	if !IsDangerous(plain) {
		t.Fatalf("未声明 Dangerous 时应按 true（保守，走审批）")
	}

	declared := &fakeTool{
		name: "declared", desc: "d",
		readOnly: true, concurrent: true, dangerous: false,
		declaresRO: true, declaresCS: true, declaresDGR: true,
	}
	if !IsReadOnly(declared) || !IsConcurrencySafe(declared) {
		t.Fatalf("已声明的属性应生效")
	}
	if IsDangerous(declared) {
		t.Fatalf("显式声明 dangerous=false 时应按 false")
	}
}

func Test_F41_DefinitionsMatchLLMToolSpec(t *testing.T) {
	t.Parallel()
	r := New()
	r.MustRegister(newFake("weather"))
	defs := r.Definitions()
	if len(defs) != 1 {
		t.Fatalf("definitions: %d", len(defs))
	}
	// 类型无需断言：Definitions 的签名已经保证返回 llm.ToolSpec，这里只验证内容。
	spec := defs[0]
	if spec.Name != "weather" {
		t.Fatalf("name: %q", spec.Name)
	}
	if len(spec.Parameters) == 0 {
		t.Fatalf("parameters 不应为空")
	}
}
