package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

// Test_F48_SimilarityCatchesParaphrases 用**实测出现过的**那对文本做用例。
//
// 事故现场：规则触发写入「我喜欢喝橙汁」，模型随后写入「用户喜欢喝橙汁」，
// 精确比较拦不住，于是同一条记忆存了两遍。
func Test_F48_SimilarityCatchesParaphrases(t *testing.T) {
	t.Parallel()
	dup := [][2]string{
		{"我喜欢喝橙汁", "用户喜欢喝橙汁"},
		{"我喜欢喝橙汁", "我喜欢喝橙汁"},
		{"对方的生日是 3 月 5 日", "生日是 3 月 5 日"},
	}
	for _, pair := range dup {
		if !IsDuplicateMemory(pair[0], pair[1]) {
			t.Fatalf("应判为同一条: %q vs %q（相似度 %.2f）", pair[0], pair[1], Similarity(pair[0], pair[1]))
		}
	}

	distinct := [][2]string{
		{"我喜欢喝橙汁", "我喜欢喝冰美式"},
		{"讨厌下雨天", "喜欢橘子味"},
		{"周五交作业", "周六打球"},
	}
	for _, pair := range distinct {
		if IsDuplicateMemory(pair[0], pair[1]) {
			t.Fatalf("不该合并成同一条: %q vs %q（相似度 %.2f）", pair[0], pair[1], Similarity(pair[0], pair[1]))
		}
	}
}

func Test_F48_StoreDedupesBySimilarity(t *testing.T) {
	t.Parallel()
	m := NewMemoryStore(0)
	ctx := WithMemoryScope(context.Background(), "s")
	if err := m.Save(ctx, "我喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := m.Save(ctx, "用户喜欢喝橙汁"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := m.Recall(ctx)
	if len(got) != 1 {
		t.Fatalf("近似重复应被去重: %v", got)
	}
}

// Test_F48_SameTurnSuppressionStopsDoubleWrite 是根因修复的回归测试。
func Test_F48_SameTurnSuppressionStopsDoubleWrite(t *testing.T) {
	t.Parallel()
	mem := NewMemoryStore(0)
	key := session.Key{SelfID: 1, UserID: 100}
	ctx := WithMemoryScope(context.Background(), key.String())

	// 1) 规则触发先写入。
	fact, ok := NewMemoryCommand(nil).Extract("记住：我喜欢喝橙汁")
	if !ok {
		t.Fatalf("应识别出记忆指令")
	}
	if err := mem.Save(ctx, fact); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 2) 本轮已捕获，模型再调 save_memory 必须是无害的。
	r := tool.New()
	if err := RegisterVirtual(r, mem); err != nil {
		t.Fatalf("RegisterVirtual: %v", err)
	}
	save, _ := r.Get(ActionSaveMemory)
	runCtx := WithMemoryScope(WithMemoryCaptured(context.Background()), key.String())
	res, err := save.Execute(runCtx, json.RawMessage(`{"text":"用户喜欢喝橙汁"}`))
	if err != nil {
		t.Fatalf("Execute 不应返回 error: %v", err)
	}
	if res.Failed() {
		t.Fatalf("重复保存不该报错，应是无害的空操作: %+v", res)
	}
	if !MemoryCaptured(runCtx) {
		t.Fatalf("标记未生效")
	}

	got, _ := mem.Recall(ctx)
	if len(got) != 1 {
		t.Fatalf("同一条记忆只应存在一份: %v", got)
	}

	// 3) 没有标记时（例如模型自主判断要记），仍应正常写入。
	free := WithMemoryScope(context.Background(), key.String())
	res, _ = save.Execute(free, json.RawMessage(`{"text":"讨厌香菜"}`))
	if res.Failed() {
		t.Fatalf("无标记时应正常写入: %+v", res)
	}
	got, _ = mem.Recall(ctx)
	if len(got) != 2 {
		t.Fatalf("应新增一条不同的事实: %v", got)
	}
}
