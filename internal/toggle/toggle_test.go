package toggle

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/router"
)

// failingStore 模拟读取失败：Store.Get 无 error 返回，故以 panic 表达。
type failingStore struct{}

func (failingStore) Get(Key) (bool, bool) { panic("disk read error") }
func (failingStore) Set(Key, bool) error  { return errors.New("disk full") }
func (failingStore) Delete(Key) error     { return errors.New("disk full") }
func (failingStore) All() ([]Key, error)  { return nil, errors.New("disk read error") }

func ctxFor(groupID int64) *router.Ctx {
	return router.NewCtx(nil, &event.Event{GroupID: groupID}, nil)
}

func Test_F19_RuleRejectsOffGroup(t *testing.T) {
	tg := New(NewMemoryStore(), true, nil)
	tg.Register("echo")
	if err := tg.Set("echo", 100, false); err != nil {
		t.Fatalf("Set off: %v", err)
	}
	if err := tg.Set("echo", 200, true); err != nil {
		t.Fatalf("Set on: %v", err)
	}
	rule := tg.Rule("echo")

	if rule(ctxFor(100)) {
		t.Fatalf("group 100 已关闭，Rule 应拒绝")
	}
	if !rule(ctxFor(200)) {
		t.Fatalf("group 200 已开启，Rule 应放行")
	}
	if !rule(ctxFor(300)) {
		t.Fatalf("group 300 未设置，defaultOn=true 应放行")
	}
	if !rule(ctxFor(0)) {
		t.Fatalf("私聊（GroupID=0）应放行")
	}
	if !rule(nil) {
		t.Fatalf("nil 上下文应放行")
	}
	if !rule(router.NewCtx(nil, nil, nil)) {
		t.Fatalf("nil Event 应放行")
	}
}

func Test_F19_FileStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toggle.json")
	fs1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs1.Set(Key{Plugin: "echo", GroupID: 7}, false); err != nil {
		t.Fatalf("Set off: %v", err)
	}
	if err := fs1.Set(Key{Plugin: "echo", GroupID: 8}, true); err != nil {
		t.Fatalf("Set on: %v", err)
	}

	// 重新打开同一路径，模拟重启。
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if on, found := fs2.Get(Key{Plugin: "echo", GroupID: 7}); !found || on {
		t.Fatalf("重启后 group 7 = (%v,%v)，期望 (false,true)", on, found)
	}
	if on, found := fs2.Get(Key{Plugin: "echo", GroupID: 8}); !found || !on {
		t.Fatalf("重启后 group 8 = (%v,%v)，期望 (true,true)", on, found)
	}
	if _, found := fs2.Get(Key{Plugin: "echo", GroupID: 9}); found {
		t.Fatalf("未设置的 group 9 不应存在")
	}

	fs3, err := NewFileStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("缺失文件应返回空 Store: %v", err)
	}
	if _, found := fs3.Get(Key{Plugin: "x", GroupID: 1}); found {
		t.Fatalf("空 Store 不应有数据")
	}
}

func Test_F19_UnknownPlugin(t *testing.T) {
	tg := New(NewMemoryStore(), true, nil)
	tg.Register("known")

	if err := tg.Validate("known"); err != nil {
		t.Fatalf("已注册插件应通过 Validate: %v", err)
	}
	if err := tg.Validate("ghost"); err == nil {
		t.Fatalf("未注册插件 Validate 应报错")
	}
	if err := tg.Set("ghost", 1, true); err == nil {
		t.Fatalf("未注册插件 Set 应报错")
	}
}

func Test_F19_StoreReadFailureFallsBackAndWarns(t *testing.T) {
	var warns []string
	tg := New(failingStore{}, true, func(s string) { warns = append(warns, s) })
	tg.Register("echo")
	if !tg.IsOn("echo", 42) {
		t.Fatalf("读失败时 defaultOn=true 应返回 true")
	}
	if len(warns) == 0 {
		t.Fatalf("读失败必须触发告警回调")
	}

	var warns2 []string
	tg2 := New(failingStore{}, false, func(s string) { warns2 = append(warns2, s) })
	if tg2.IsOn("echo", 42) {
		t.Fatalf("读失败时 defaultOn=false 应返回 false")
	}
	if len(warns2) == 0 {
		t.Fatalf("读失败必须触发告警回调")
	}

	// nil 回调允许，且 nil store 同样告警降级。
	tg3 := New(nil, true, nil)
	if !tg3.IsOn("echo", 42) {
		t.Fatalf("nil store 也应回落 defaultOn")
	}
}

func Test_F19_EnabledGroupsOnlyOn(t *testing.T) {
	s := NewMemoryStore()
	tg := New(s, false, nil)
	tg.Register("echo", "ping")
	for _, kv := range []struct {
		g  int64
		on bool
	}{{10, true}, {30, true}, {20, false}} {
		if err := tg.Set("echo", kv.g, kv.on); err != nil {
			t.Fatalf("Set(%d): %v", kv.g, err)
		}
	}
	if err := tg.Set("ping", 40, true); err != nil {
		t.Fatalf("Set ping: %v", err)
	}

	got := tg.EnabledGroups("echo")
	if len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Fatalf("EnabledGroups(echo) = %v，期望 [10 30]", got)
	}
	if got := tg.EnabledGroups("ping"); len(got) != 1 || got[0] != 40 {
		t.Fatalf("EnabledGroups(ping) = %v，期望 [40]", got)
	}
	if got := tg.EnabledGroups("echo"); len(got) != 2 {
		t.Fatalf("只应包含显式开启的群")
	}

	// All 读取失败时告警且返回 nil。
	var warned bool
	tg2 := New(failingStore{}, false, func(string) { warned = true })
	if got := tg2.EnabledGroups("echo"); got != nil {
		t.Fatalf("All 失败应返回 nil，得到 %v", got)
	}
	if !warned {
		t.Fatalf("All 失败应告警")
	}
}
