package moderation

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func Test_F58_BanImmediateAndExpire(t *testing.T) {
	clock := newFakeClock()
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	if err := bl.Ban(BanUser, "42", "刷屏", time.Minute); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	entry, ok := bl.Banned(BanUser, "42")
	if !ok {
		t.Fatal("拉黑后应立即生效")
	}
	if entry.Reason != "刷屏" || entry.ExpiresAt.IsZero() {
		t.Fatalf("条目不符: %+v", entry)
	}
	ctx := context.Background()
	eng := New(Options{Blacklist: bl})
	d, _ := eng.Review(ctx, Message{Text: "hello"}, Meta{UserID: 42, Addressed: true})
	if !d.Blocked() || d.Rule != "blacklist:user" {
		t.Fatalf("引擎应拦截黑名单用户: %+v", d)
	}

	clock.Advance(time.Minute)
	if _, ok := bl.Banned(BanUser, "42"); ok {
		t.Fatal("过期后应自动恢复")
	}
	d, _ = eng.Review(ctx, Message{Text: "hello"}, Meta{UserID: 42, Addressed: true})
	if !d.Allowed() {
		t.Fatalf("过期后引擎应放行: %+v", d)
	}
}

func Test_F58_FileStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bans.json")
	clock := newFakeClock()

	store, err := NewFileBanStore(path)
	if err != nil {
		t.Fatalf("NewFileBanStore: %v", err)
	}
	bl, err := NewBlacklist(store, BlacklistOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	if err := bl.Ban(BanGroup, "123", "广告", time.Hour); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := bl.Ban(BanUser, "7", "永久", 0); err != nil {
		t.Fatalf("Ban: %v", err)
	}

	// 模拟重启：重新打开同一文件。
	store2, err := NewFileBanStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	bl2, err := NewBlacklist(store2, BlacklistOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist reload: %v", err)
	}
	if _, ok := bl2.Banned(BanGroup, "123"); !ok {
		t.Fatal("重启后群封禁应保留")
	}
	if _, ok := bl2.Banned(BanUser, "7"); !ok {
		t.Fatal("重启后永久封禁应保留")
	}
	clock.Advance(2 * time.Hour)
	if _, ok := bl2.Banned(BanGroup, "123"); ok {
		t.Fatal("带过期的封禁过期后应恢复")
	}
	if _, ok := bl2.Banned(BanUser, "7"); !ok {
		t.Fatal("永久封禁不应过期")
	}
	if _, err := NewFileBanStore(""); err == nil {
		t.Fatal("空路径应返回 error")
	}
}

func Test_F58_ProtectedWhitelistPriority(t *testing.T) {
	store := NewMemoryBanStore()
	// 即使存储里已存在对超管的封禁，查询也必须放行。
	if err := store.Add(BanEntry{Kind: BanUser, ID: "200", Reason: "伪造"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	bl, err := NewBlacklist(store, BlacklistOptions{SelfID: 100, SuperUsers: []int64{200}})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	if _, ok := bl.Banned(BanUser, "200"); ok {
		t.Fatal("超管应始终视为未封禁")
	}
	if err := bl.Ban(BanUser, "100", "x", 0); !errors.Is(err, ErrProtected) {
		t.Fatalf("机器人自身不可封禁，得到 %v", err)
	}
	if err := bl.Ban(BanUser, "200", "x", 0); !errors.Is(err, ErrProtected) {
		t.Fatalf("超管不可封禁，得到 %v", err)
	}
	if _, ok := bl.Banned(BanUser, "100"); ok {
		t.Fatal("机器人自身不应被封禁")
	}
}

func Test_F58_UnbanAndList(t *testing.T) {
	clock := newFakeClock()
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	if err := bl.Ban(BanUser, "1", "a", 0); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := bl.Ban(BanIP, "10.0.0.1", "b", time.Minute); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if got := len(bl.List()); got != 2 {
		t.Fatalf("List 应有 2 条，得到 %d", got)
	}
	existed, err := bl.Unban(BanUser, "1")
	if err != nil || !existed {
		t.Fatalf("Unban 应报告存在: %v %v", existed, err)
	}
	again, err := bl.Unban(BanUser, "1")
	if err != nil || again {
		t.Fatalf("重复 Unban 应报告不存在: %v %v", again, err)
	}
}

func Test_F58_EngineBlacklistGroupAndIP(t *testing.T) {
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	if err := bl.Ban(BanGroup, "55", "group-spam", 0); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := bl.Ban(BanIP, "10.1.2.3", "ip-spam", 0); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	eng := New(Options{Blacklist: bl})
	ctx := context.Background()
	if d, _ := eng.Review(ctx, Message{Text: "x"}, Meta{UserID: 1, GroupID: 55, Addressed: true}); !d.Blocked() {
		t.Fatalf("群封禁应拦截: %+v", d)
	}
	if d, _ := eng.Review(ctx, Message{Text: "x"}, Meta{UserID: 1, IP: "10.1.2.3", Addressed: true}); !d.Blocked() {
		t.Fatalf("IP 封禁应拦截: %+v", d)
	}
}

func Test_F58_CommandsRequireSuperUser(t *testing.T) {
	clock := newFakeClock()
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{SelfID: 100, SuperUsers: []int64{999}, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	cmd := NewCommander(bl, AuthorizerFunc(func(id int64) bool { return id == 999 }), CommanderOptions{Now: clock.Now})

	res, err := cmd.Handle("/ban 42 60s", Meta{UserID: 999, Addressed: true})
	if err != nil || !res.Handled || !strings.Contains(res.Reply, "已封禁") {
		t.Fatalf("/ban 失败: %+v %v", res, err)
	}
	if _, ok := bl.Banned(BanUser, "42"); !ok {
		t.Fatal("/ban 后应生效")
	}

	res, err = cmd.Handle("/banlist", Meta{UserID: 999})
	if err != nil || !strings.Contains(res.Reply, "42") {
		t.Fatalf("/banlist 失败: %+v %v", res, err)
	}

	res, err = cmd.Handle("/unban 42", Meta{UserID: 999})
	if err != nil || !strings.Contains(res.Reply, "已解封") {
		t.Fatalf("/unban 失败: %+v %v", res, err)
	}
	if _, ok := bl.Banned(BanUser, "42"); ok {
		t.Fatal("/unban 后应解除")
	}

	if _, err := cmd.Handle("/ban 55", Meta{UserID: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非超管应被拒绝: %v", err)
	}
	if _, ok := bl.Banned(BanUser, "55"); ok {
		t.Fatal("未授权命令不得产生封禁")
	}

	if _, err := cmd.Handle("/ban 100", Meta{UserID: 999}); !errors.Is(err, ErrProtected) {
		t.Fatalf("受保护主体应被拒绝: %v", err)
	}
	if _, err := cmd.Handle("/ban bad-id", Meta{UserID: 999}); !errors.Is(err, ErrBadArgs) {
		t.Fatalf("非法参数应返回 ErrBadArgs: %v", err)
	}
	if _, err := cmd.Handle("/ban 42 nope", Meta{UserID: 999}); !errors.Is(err, ErrBadDuration) {
		t.Fatalf("非法时长应返回 ErrBadDuration: %v", err)
	}

	if res, err := cmd.Handle("/ban group 123 1m", Meta{UserID: 999}); err != nil || !res.Handled {
		t.Fatalf("/ban group 失败: %+v %v", res, err)
	}
	if _, ok := bl.Banned(BanGroup, "123"); !ok {
		t.Fatal("群封禁应生效")
	}
	if res, err := cmd.Handle("/ban ip 10.0.0.1", Meta{UserID: 999}); err != nil || !res.Handled {
		t.Fatalf("/ban ip 失败: %+v %v", res, err)
	}
	if _, ok := bl.Banned(BanIP, "10.0.0.1"); !ok {
		t.Fatal("IP 封禁应生效")
	}

	plain, err := cmd.Handle("hello world", Meta{UserID: 999})
	if err != nil || plain.Handled {
		t.Fatalf("非命令不应被处理: %+v %v", plain, err)
	}
}

func Test_F58_ConcurrentBanLookup(t *testing.T) {
	bl, err := NewBlacklist(NewMemoryBanStore(), BlacklistOptions{})
	if err != nil {
		t.Fatalf("NewBlacklist: %v", err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				id := strconv.Itoa((seed + i) % 40)
				if i%5 == 0 {
					if err := bl.Ban(BanUser, id, "x", time.Minute); err != nil && !errors.Is(err, ErrProtected) {
						t.Errorf("Ban: %v", err)
						return
					}
				}
				bl.Banned(BanUser, id)
				bl.List()
			}
		}(g)
	}
	wg.Wait()
}
