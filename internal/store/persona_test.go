package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Test_F82_PersonaPersistsAcrossReopen 覆盖 F-82 的持久化要求：
// 切换人格后重启进程（重开数据库）仍能读回，而不是"重启即回到默认"。
func Test_F82_PersonaPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persona.db")

	first, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	key := "v1:5:group/1:2:u42"
	if _, ok, err := first.GetPersona(ctx, key); err != nil || ok {
		t.Fatalf("未设置时应返回 ok=false: ok=%v err=%v", ok, err)
	}
	if err := first.SetPersona(ctx, key, "x"); err != nil {
		t.Fatalf("SetPersona: %v", err)
	}
	if err := first.SetPersona(ctx, key, "y"); err != nil {
		t.Fatalf("SetPersona overwrite: %v", err)
	}
	if p, ok, err := first.GetPersona(ctx, key); err != nil || !ok || p != "y" {
		t.Fatalf("覆盖写后应读到 y: %q %v %v", p, ok, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()
	if p, ok, err := second.GetPersona(ctx, key); err != nil || !ok || p != "y" {
		t.Fatalf("重开后人格丢失: %q %v %v", p, ok, err)
	}
	deleted, err := second.DeletePersona(ctx, key)
	if err != nil || !deleted {
		t.Fatalf("DeletePersona=(%v,%v), want (true,nil)", deleted, err)
	}
	if _, ok, _ := second.GetPersona(ctx, key); ok {
		t.Fatal("删除后仍读到人格")
	}
	if deleted, err := second.DeletePersona(ctx, key); err != nil || deleted {
		t.Fatalf("重复删除应返回 false: (%v,%v)", deleted, err)
	}
}

// Test_F82_PersonaRejectsEmptyValues 钉住失败模式：
// 静默写入空人格等价于"下次启动人格被悄悄重置"，必须拒绝而不是接受。
func Test_F82_PersonaRejectsEmptyValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTest(t, Options{Path: filepath.Join(t.TempDir(), "reject.db")})

	if err := s.SetPersona(ctx, "k", "  "); err == nil {
		t.Fatal("空人格必须被拒绝")
	}
	if err := s.SetPersona(ctx, "  ", "x"); err == nil {
		t.Fatal("空会话键必须被拒绝")
	}
	if _, ok, err := s.GetPersona(ctx, "  "); err != nil || ok {
		t.Fatalf("空键读取应返回 (false,nil): ok=%v err=%v", ok, err)
	}
}
