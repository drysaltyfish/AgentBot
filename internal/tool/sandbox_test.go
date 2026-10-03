package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// sandboxSpy 是沙箱测试用工具：记录 Execute 调用次数并声明资源需求。
type sandboxSpy struct {
	name     string
	decl     SandboxRequest
	readOnly bool
	out      string
	calls    int
}

func (s *sandboxSpy) Name() string        { return s.name }
func (s *sandboxSpy) Description() string { return "" }
func (s *sandboxSpy) Parameters() Schema  { return Schema{} }
func (s *sandboxSpy) ReadOnly() bool      { return s.readOnly }
func (s *sandboxSpy) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	s.calls++
	return Success(s.out), nil
}

func (s *sandboxSpy) SandboxRequest(args json.RawMessage) SandboxRequest { return s.decl }

func Test_F46_DeniesWriteOutsideRootWithoutExecuting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	spy := &sandboxSpy{name: "write_file", decl: SandboxRequest{
		WritePaths: []string{filepath.Join(root, "outside", "x.txt")},
	}}
	st, err := NewSandboxTool(spy, &Policy{WriteRoots: []string{filepath.Join(root, "rw")}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("拒绝不应返回 error: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("越权调用不得执行内层工具，实际调用 %d 次", spy.calls)
	}
	if !res.Failed() {
		t.Fatalf("越权应返回失败结果")
	}
	if got := res.Metadata["code"]; got != DenyWritePath {
		t.Fatalf("拒绝码: actual=%q expected=%q", got, DenyWritePath)
	}
}

func Test_F46_AllowsWriteWithinRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rw := filepath.Join(root, "rw")
	spy := &sandboxSpy{name: "write_file", out: "ok", decl: SandboxRequest{
		WritePaths: []string{filepath.Join(rw, "a", "b.txt")},
	}}
	st, err := NewSandboxTool(spy, &Policy{WriteRoots: []string{rw}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, err := st.Execute(context.Background(), nil)
	if err != nil || res.Failed() {
		t.Fatalf("白名单内不应拒绝: res=%+v err=%v", res, err)
	}
	if spy.calls != 1 {
		t.Fatalf("白名单内应执行一次，实际 %d", spy.calls)
	}
}

func Test_F46_ReadRootAndWriteRootSemantics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rOnly := filepath.Join(root, "ro")
	rw := filepath.Join(root, "rw")

	spyRead := &sandboxSpy{name: "read", decl: SandboxRequest{ReadPaths: []string{filepath.Join(rOnly, "a.txt")}}}
	stRead, err := NewSandboxTool(spyRead, &Policy{ReadRoots: []string{rOnly}, WriteRoots: []string{rw}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := stRead.Execute(context.Background(), nil); res.Failed() {
		t.Fatalf("只读根内应放行: %+v", res)
	}

	spyWrite := &sandboxSpy{name: "write", decl: SandboxRequest{WritePaths: []string{filepath.Join(rOnly, "a.txt")}}}
	stWrite, err := NewSandboxTool(spyWrite, &Policy{ReadRoots: []string{rOnly}, WriteRoots: []string{rw}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := stWrite.Execute(context.Background(), nil); !res.Failed() {
		t.Fatalf("只读根下写入必须被拒绝: %+v", res)
	}

	spyRW := &sandboxSpy{name: "rw", decl: SandboxRequest{ReadPaths: []string{filepath.Join(rw, "a.txt")}}}
	stRW, err := NewSandboxTool(spyRW, &Policy{WriteRoots: []string{rw}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := stRW.Execute(context.Background(), nil); res.Failed() {
		t.Fatalf("可写根应视为可读: %+v", res)
	}
}

func Test_F46_EnvAllowlist(t *testing.T) {
	t.Parallel()
	spy := &sandboxSpy{name: "run", decl: SandboxRequest{Env: []string{"PATH", "SECRET"}}}
	st, err := NewSandboxTool(spy, &Policy{EnvAllowlist: []string{"PATH"}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, _ := st.Execute(context.Background(), nil)
	if !res.Failed() || res.Metadata["code"] != DenyEnv {
		t.Fatalf("未在环境白名单内的变量应被拒绝: %+v", res)
	}
	if spy.calls != 0 {
		t.Fatalf("拒绝时不得执行")
	}
}

func Test_F46_NetworkDeniedByDefaultEnabledPerTool(t *testing.T) {
	t.Parallel()
	net := &sandboxSpy{name: "fetch", decl: SandboxRequest{Network: true}}
	st, err := NewSandboxTool(net, &Policy{})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := st.Execute(context.Background(), nil); !res.Failed() || res.Metadata["code"] != DenyNetwork {
		t.Fatalf("默认应禁止网络: %+v", res)
	}

	net2 := &sandboxSpy{name: "fetch", decl: SandboxRequest{Network: true}}
	st2, err := NewSandboxTool(net2, &Policy{NetworkTools: []string{"fetch"}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := st2.Execute(context.Background(), nil); res.Failed() {
		t.Fatalf("显式开启网络后应放行: %+v", res)
	}
}

func Test_F46_ForbiddenOperation(t *testing.T) {
	t.Parallel()
	spy := &sandboxSpy{name: "run", decl: SandboxRequest{Ops: []string{"rm -rf"}}}
	st, err := NewSandboxTool(spy, &Policy{ForbiddenOps: []string{"rm -rf"}})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, _ := st.Execute(context.Background(), nil)
	if !res.Failed() || res.Metadata["code"] != DenyOperation {
		t.Fatalf("危险操作应被拒绝: %+v", res)
	}
	if res.Metadata["field"] != "rm -rf" {
		t.Fatalf("拒绝应带具体操作名: %+v", res.Metadata)
	}
}

func Test_F46_RequireReadOnly(t *testing.T) {
	t.Parallel()
	danger := &sandboxSpy{name: "mutation", readOnly: false}
	st, err := NewSandboxTool(danger, &Policy{RequireReadOnly: true})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, _ := st.Execute(context.Background(), nil)
	if !res.Failed() || res.Metadata["code"] != DenyReadOnly {
		t.Fatalf("非只读工具应被拒绝: %+v", res)
	}
	if danger.calls != 0 {
		t.Fatalf("拒绝时不得执行")
	}

	ro := &sandboxSpy{name: "query", readOnly: true}
	st2, err := NewSandboxTool(ro, &Policy{RequireReadOnly: true})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if res, _ := st2.Execute(context.Background(), nil); res.Failed() {
		t.Fatalf("只读工具应放行: %+v", res)
	}
	if !st2.ReadOnly() {
		t.Fatalf("包装器必须透传只读声明")
	}
}

func Test_F46_OutputIsTruncatedWithinLimit(t *testing.T) {
	t.Parallel()
	spy := &sandboxSpy{name: "dump", out: strings.Repeat("很", 2000)}
	st, err := NewSandboxTool(spy, &Policy{MaxOutputBytes: 100})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	res, err := st.Execute(context.Background(), nil)
	if err != nil || res.Failed() {
		t.Fatalf("截断不应算失败: res=%+v err=%v", res, err)
	}
	if len(res.Output) > 100 {
		t.Fatalf("输出应 <=100 字节，实际 %d", len(res.Output))
	}
	if !utf8.ValidString(res.Output) {
		t.Fatalf("截断不得切坏 UTF-8：%q", res.Output)
	}
	if !strings.HasSuffix(res.Output, sandboxTruncatedMarker) {
		t.Fatalf("截断应带标记: %q", res.Output)
	}
	if res.Metadata["sandbox_truncated"] != "output" {
		t.Fatalf("截断应写 Metadata: %+v", res.Metadata)
	}
}

func Test_F46_InvalidPolicyRejectedAtStartup(t *testing.T) {
	t.Parallel()
	cases := []*Policy{
		{MaxOutputBytes: -1},
		{ReadRoots: []string{"relative/path"}},
		{EnvAllowlist: []string{"HAS=EQ"}},
		{ForbiddenOps: []string{"  "}},
	}
	for i, p := range cases {
		if err := p.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("第 %d 个非法策略应被拒绝: %v", i, err)
		}
	}
	if _, err := NewSandboxTool(nil, &Policy{}); !errors.Is(err, ErrNilTool) {
		t.Fatalf("nil 工具应返回 ErrNilTool: %v", err)
	}
	if _, err := NewSandboxTool(&sandboxSpy{name: "x"}, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("nil 策略应返回 ErrInvalidPolicy: %v", err)
	}
}

func Test_F46_DenialIsStructured(t *testing.T) {
	t.Parallel()
	d := Denial{Tool: "write", Code: DenyWritePath, Field: "/tmp/x", Detail: "越界"}
	js := d.JSON()
	for _, want := range []string{"\"tool\":\"write\"", "\"code\":\"path_not_writable\"", "\"field\":\"/tmp/x\""} {
		if !strings.Contains(js, want) {
			t.Fatalf("结构化拒绝缺少 %s: %s", want, js)
		}
	}
	res := d.Result()
	if res.Metadata["code"] != DenyWritePath || res.Metadata["denial"] != js {
		t.Fatalf("Result 元数据不完整: %+v", res.Metadata)
	}
}

func Test_F46_RegistrySandboxWrapsAllInOrder(t *testing.T) {
	t.Parallel()
	r := New()
	a := &sandboxSpy{name: "alpha", decl: SandboxRequest{Network: true}}
	b := &sandboxSpy{name: "beta", out: "ok"}
	r.MustRegister(a)
	r.MustRegister(b)

	sr, err := r.Sandbox(&Policy{})
	if err != nil {
		t.Fatalf("装配沙箱注册表失败: %v", err)
	}
	if got := strings.Join(sr.Names(), ","); got != "alpha,beta" {
		t.Fatalf("沙箱注册表必须保序: %q", got)
	}
	if r.Len() != 2 {
		t.Fatalf("原注册表不应改变: %d", r.Len())
	}
	tl, ok := sr.Get("alpha")
	if !ok {
		t.Fatalf("找不到 alpha")
	}
	res, _ := tl.Execute(context.Background(), nil)
	if !res.Failed() || res.Metadata["code"] != DenyNetwork {
		t.Fatalf("沙箱注册表内的工具应经过策略: %+v", res)
	}
	if a.calls != 0 {
		t.Fatalf("被拒绝的工具不得执行")
	}
}

func Test_F46_NilPolicyCheckDenies(t *testing.T) {
	t.Parallel()
	var p *Policy
	if d := p.Check("x", SandboxRequest{}); d == nil || d.Code != DenyForbiddenTool {
		t.Fatalf("nil 策略必须保守拒绝: %+v", d)
	}
}
