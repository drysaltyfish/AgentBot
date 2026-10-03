package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// allow 是放行一切的 Checker。
func allow(context.Context, Invocation) bool { return true }

// recorder 收集审计事件，供断言使用。
type recorder struct{ events []AuditEvent }

func (r *recorder) audit(e AuditEvent) { r.events = append(r.events, e) }

// fixedNow 返回固定的时间源，让审计时间可断言。
func fixedNow() time.Time { return time.Unix(1700000000, 0) }

func newTestModule(t *testing.T, opts Options, rec *recorder) *Module {
	t.Helper()
	if opts.Now == nil {
		opts.Now = fixedNow
	}
	if opts.Auditor == nil && rec != nil {
		opts.Auditor = rec.audit
	}
	return New(opts)
}

func Test_F71_Parse_Dispatch(t *testing.T) {
	rec := &recorder{}
	m := newTestModule(t, Options{Checker: allow, Auditor: rec.audit, Now: fixedNow}, rec)

	var got Invocation
	if err := m.Register("echo", "回显参数", func(_ context.Context, inv Invocation) (string, error) {
		got = inv
		return strings.Join(inv.Args, ","), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	reply, err := m.Dispatch(context.Background(), Request{
		Text: "/echo Hello World", UserID: 7, GroupID: 9, Source: SourceMessage,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if reply != "Hello,World" {
		t.Fatalf("reply = %q，期望参数被原样保留大小写", reply)
	}
	if got.Command != "echo" || len(got.Args) != 2 || got.UserID != 7 || got.GroupID != 9 || got.Source != SourceMessage {
		t.Fatalf("Invocation = %+v，解析不正确", got)
	}

	// CLI 入口不带前导斜杠，复用同一实现。
	reply, err = m.Dispatch(context.Background(), Request{Text: "eCho one", Source: SourceCLI})
	if err != nil {
		t.Fatalf("Dispatch CLI: %v", err)
	}
	if reply != "one" || got.Command != "echo" {
		t.Fatalf("CLI 分发失败: reply=%q inv=%+v", reply, got)
	}
	if len(rec.events) != 2 || rec.events[0].Result != "ok" {
		t.Fatalf("审计事件 = %+v", rec.events)
	}
	if rec.events[0].At != fixedNow() {
		t.Fatalf("审计时间未使用注入的时间源: %v", rec.events[0].At)
	}
}

func Test_F71_Auth(t *testing.T) {
	deny := func(context.Context, Invocation) bool { return false }

	t.Run("explicit", func(t *testing.T) {
		rec := &recorder{}
		m := newTestModule(t, Options{Checker: deny, Auditor: rec.audit, DenyMode: DenyExplicit, Now: fixedNow}, rec)
		called := false
		if err := m.Register("status", "状态", func(context.Context, Invocation) (string, error) {
			called = true
			return "secret", nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		reply, err := m.Dispatch(context.Background(), Request{Text: "/status", UserID: 1})
		if err != nil {
			t.Fatalf("明确拒绝不应返回 error: %v", err)
		}
		if reply == "" {
			t.Fatalf("明确拒绝应给出回复文本")
		}
		if called {
			t.Fatalf("未授权时不得执行处理器")
		}
		if len(rec.events) != 1 || rec.events[0].Authorized || rec.events[0].Result != "denied" {
			t.Fatalf("拒绝审计缺失: %+v", rec.events)
		}
	})

	t.Run("silent", func(t *testing.T) {
		rec := &recorder{}
		m := newTestModule(t, Options{Checker: deny, Auditor: rec.audit, DenyMode: DenySilent, Now: fixedNow}, rec)
		reply, err := m.Dispatch(context.Background(), Request{Text: "/status"})
		if err != nil || reply != "" {
			t.Fatalf("静默拒绝应返回空串且无 error: reply=%q err=%v", reply, err)
		}
		if len(rec.events) != 1 || rec.events[0].Authorized || rec.events[0].Result != "denied" {
			t.Fatalf("静默拒绝也必须记审计: %+v", rec.events)
		}
	})

	t.Run("nil checker denies", func(t *testing.T) {
		rec := &recorder{}
		m := newTestModule(t, Options{Auditor: rec.audit, Now: fixedNow}, rec)
		reply, err := m.Dispatch(context.Background(), Request{Text: "/help"})
		if err != nil || reply != "" {
			t.Fatalf("Checker 为 nil 应 fail-closed: reply=%q err=%v", reply, err)
		}
	})
}

func Test_F71_UnknownCommand(t *testing.T) {
	rec := &recorder{}
	m := newTestModule(t, Options{Checker: allow, Auditor: rec.audit, Now: fixedNow}, rec)
	reply, err := m.Dispatch(context.Background(), Request{Text: "/nope"})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("错误 = %v，期望 ErrUnknownCommand", err)
	}
	if reply != "" {
		t.Fatalf("未知命令回复 = %q，期望空串", reply)
	}
	if len(rec.events) != 1 || rec.events[0].Result != "error" {
		t.Fatalf("未知命令也应记审计: %+v", rec.events)
	}
}

func Test_F71_Help(t *testing.T) {
	m := newTestModule(t, Options{Checker: allow, Now: fixedNow}, nil)
	if err := m.Register("echo", "回显参数", func(context.Context, Invocation) (string, error) { return "", nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := m.Dispatch(context.Background(), Request{Text: "/help"})
	if err != nil {
		t.Fatalf("Dispatch /help: %v", err)
	}
	if !strings.Contains(reply, "help") || !strings.Contains(reply, "echo") || !strings.Contains(reply, "回显参数") {
		t.Fatalf("/help 输出不完整:\n%s", reply)
	}

	cmds := m.Commands()
	if len(cmds) != 2 {
		t.Fatalf("Commands() 数量 = %d，期望 2", len(cmds))
	}
	if cmds[0].Name != "echo" || cmds[1].Name != "help" {
		t.Fatalf("Commands() 未按名称排序: %+v", cmds)
	}
}

func Test_F71_MultiWordCommand(t *testing.T) {
	m := newTestModule(t, Options{Checker: allow, Now: fixedNow}, nil)
	var hit string
	if err := m.Register("config", "配置", func(_ context.Context, inv Invocation) (string, error) {
		hit = "config:" + strings.Join(inv.Args, ",")
		return hit, nil
	}); err != nil {
		t.Fatalf("Register config: %v", err)
	}
	if err := m.Register("config reload", "重载配置", func(_ context.Context, inv Invocation) (string, error) {
		hit = "reload"
		return hit, nil
	}); err != nil {
		t.Fatalf("Register config reload: %v", err)
	}
	if _, err := m.Dispatch(context.Background(), Request{Text: "/config reload"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if hit != "reload" {
		t.Fatalf("应按最长前缀匹配到双词命令，实际命中 %q", hit)
	}
}

func Test_F71_Builtins(t *testing.T) {
	rec := &recorder{}
	m := newTestModule(t, Options{Checker: allow, Auditor: rec.audit, Now: fixedNow}, rec)
	reloaded := 0

	err := m.RegisterBuiltins(Builtins{
		Status: func(context.Context) (Status, error) {
			return Status{Uptime: 90 * time.Second, Events: 42, ActiveSessions: 3, QueueDepth: 5, ErrorRate: 0.125}, nil
		},
		Routes: func(context.Context) ([]RouteInfo, error) {
			return []RouteInfo{{Name: "echo", Kind: "message/group", Priority: 10, Once: true}}, nil
		},
		Cost: func(_ context.Context, scope string) (Cost, error) {
			return Cost{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, AmountUSD: 0.5}, nil
		},
		Reload:     func(context.Context) error { reloaded++; return nil },
		PromptHash: func(context.Context) ([]HashSegment, error) { return []HashSegment{{Name: "system", Hash: "abc"}}, nil },
	})
	if err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	cases := []struct {
		text string
		want string
	}{
		{"/status", "活跃会话：3"},
		{"/routes", "Once=是"},
		{"/cost today", "total=120"},
		{"/config reload", "配置已重载。"},
		{"/prompt-hash", "system: abc"},
	}
	for _, tc := range cases {
		reply, derr := m.Dispatch(context.Background(), Request{Text: tc.text})
		if derr != nil {
			t.Fatalf("Dispatch %q: %v", tc.text, derr)
		}
		if !strings.Contains(reply, tc.want) {
			t.Fatalf("Dispatch %q 回复 %q，期望包含 %q", tc.text, reply, tc.want)
		}
	}
	if reloaded != 1 {
		t.Fatalf("reload 调用次数 = %d，期望 1", reloaded)
	}

	// 参数校验走 ErrUsage。
	if _, uerr := m.Dispatch(context.Background(), Request{Text: "/cost hourly"}); !errors.Is(uerr, ErrUsage) {
		t.Fatalf("/cost 非法范围错误 = %v，期望 ErrUsage", uerr)
	}
	if _, uerr := m.Dispatch(context.Background(), Request{Text: "/config now"}); !errors.Is(uerr, ErrUsage) {
		t.Fatalf("/config 非法子命令错误 = %v，期望 ErrUsage", uerr)
	}

	// 数据源错误必须向上传递并保留 %w 链。
	wantErr := errors.New("boom")
	m2 := newTestModule(t, Options{Checker: allow, Now: fixedNow}, nil)
	if rerr := m2.RegisterBuiltins(Builtins{Reload: func(context.Context) error { return wantErr }}); rerr != nil {
		t.Fatalf("RegisterBuiltins: %v", rerr)
	}
	if _, derr := m2.Dispatch(context.Background(), Request{Text: "/config reload"}); !errors.Is(derr, wantErr) {
		t.Fatalf("reload 错误 = %v，期望包含 %v", derr, wantErr)
	}
}

func Test_F71_Truncate_And_Filter(t *testing.T) {
	rec := &recorder{}
	m := newTestModule(t, Options{
		Checker:  allow,
		Auditor:  rec.audit,
		Now:      fixedNow,
		MaxReply: 20,
		Filter:   func(s string) string { return "[f]" + s },
	}, rec)
	if err := m.Register("long", "长输出", func(context.Context, Invocation) (string, error) {
		return strings.Repeat("x", 100), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := m.Dispatch(context.Background(), Request{Text: "/long"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.HasPrefix(reply, "[f]") {
		t.Fatalf("出口过滤未生效: %q", reply)
	}
	if got := len([]rune(reply)); got > 20 {
		t.Fatalf("回复长度 = %d，超过 MaxReply 20", got)
	}
	if !strings.HasSuffix(reply, "…(已截断)") {
		t.Fatalf("截断标记缺失: %q", reply)
	}
}

func Test_F71_Register_Validation(t *testing.T) {
	m := newTestModule(t, Options{Checker: allow, Now: fixedNow}, nil)
	if err := m.Register("", "空", func(context.Context, Invocation) (string, error) { return "", nil }); err == nil {
		t.Fatalf("空命令名应报错")
	}
	if err := m.Register("x", "空处理器", nil); err == nil {
		t.Fatalf("nil 处理器应报错")
	}
	// 覆盖注册：后注册者生效。
	if err := m.Register("dup", "一", func(context.Context, Invocation) (string, error) { return "1", nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.Register("dup", "二", func(context.Context, Invocation) (string, error) { return "2", nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := m.Dispatch(context.Background(), Request{Text: "/dup"})
	if err != nil || reply != "2" {
		t.Fatalf("覆盖注册未生效: reply=%q err=%v", reply, err)
	}
	if err := m.Register("  ", "空白", func(context.Context, Invocation) (string, error) { return "", nil }); err == nil {
		t.Fatalf("全空白命令名应报错")
	}
}
