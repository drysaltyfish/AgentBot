package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/scope"
)

// Test_F49_SaveSupersedesSimilarWorkingItem 是"更正不生效"的回归测试。
//
// 真实事故：群里有人说"他是奶味辰辰酱"，记忆一直回灌；本人后来要求改称呼，
// 机器人还是叫旧名字。原因是 Save 只做 AppendWorking——更正变成第二条，
// 旧那条照样被召回。现在同一归属人下的相似事实必须**就地改写**。
func Test_F49_SaveSupersedesSimilarWorkingItem(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{})
	ctx := scope.WithSubject(scope.WithScope(context.Background(), "g"), 42)

	first := "张三很怕辣，一点辣都不吃"
	second := "张三很怕辣，一点辣都不吃。" // 只差一个句号：同一件事的改写
	if err := tm.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := tm.Save(ctx, second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}

	items, err := tm.Working(ctx)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("相似事实应就地改写而不是追加，得到 %d 条: %+v", len(items), items)
	}
	if items[0].Text != second {
		t.Fatalf("应保留最新正文，得到 %q", items[0].Text)
	}
	if items[0].SubjectID != 42 {
		t.Fatalf("归属人应落到 tier 条目上，得到 %d", items[0].SubjectID)
	}
}

// Test_F49_SaveDoesNotMergeAcrossSubjects 钉住"只在同一归属人之间比较"。
//
// 不限定归属人时，「张三很怕辣」与「李四很怕辣」会被相似度判为同一条，
// 把两个人的事混在一起——那是无法事后拆开的数据损失（F-87 的既有约束）。
func Test_F49_SaveDoesNotMergeAcrossSubjects(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{})
	base := context.Background()
	zhang := scope.WithSubject(scope.WithScope(base, "g"), 1001)
	li := scope.WithSubject(scope.WithScope(base, "g"), 1002)

	// 文本完全相同，只有归属人不同：仍然必须是两条。
	if err := tm.Save(zhang, "很怕辣"); err != nil {
		t.Fatalf("Save(zhang): %v", err)
	}
	if err := tm.Save(li, "很怕辣"); err != nil {
		t.Fatalf("Save(li): %v", err)
	}

	items, err := tm.Working(zhang)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("不同归属人的事实不得合并，得到 %d 条: %+v", len(items), items)
	}
}

// Test_F49_MessagesWithOrdinalSuffixStayDistinct 钉住缓冲层门槛。
//
// "片段消息-0"与"片段消息-1"的字符二元组 Jaccard 有 0.67，用 F-87 的 0.50
// 门槛会把整段缓冲合并成一条，固化与片段切分就永远不会发生。Working 层的门槛
// 因此更高（0.75），短文本更只认"完全相同/高度相似"。
func Test_F49_MessagesWithOrdinalSuffixStayDistinct(t *testing.T) {
	t.Parallel()
	// 用默认 Working 上限（50）：这里要观察的是"没被合并"，不是裁剪。
	tm := NewTiered(TieredOptions{})
	ctx := scope.WithScope(context.Background(), "g")
	for i := 0; i < 4; i++ {
		if err := tm.Save(ctx, "片段消息-"+string(rune('0'+i))); err != nil {
			t.Fatalf("Save(%d): %v", i, err)
		}
	}
	items, err := tm.Working(ctx)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("带序号的短片段不应互相吞并，得到 %d 条: %+v", len(items), items)
	}
}

// Test_F49_IdentityFactsAreNotStored 钉住"称呼类事实不入记忆"。
//
// 称呼与群名片是平台事实（每条消息都带当前名片）。抄进记忆只会得到一份
// 每轮回灌、改名后仍压着新名片的过期副本，而且不同发言人写入的归属人各不相同，
// 写入侧的覆盖判定在结构上救不了——只能在入口挡住。
func Test_F49_IdentityFactsAreNotStored(t *testing.T) {
	t.Parallel()
	tm := NewTiered(TieredOptions{})
	ctx := scope.WithScope(context.Background(), "g")

	for _, text := range []string{
		"QQ3315793548 昵称是奶味辰辰酱",
		"奶味辰辰酱(QQ3315793548)要求以后称呼他为辰辰欧巴",
		"灰色愿望机(QQ1178164715)的名字是superwoman",
	} {
		if err := tm.Save(ctx, text); !errors.Is(err, ErrIdentityFact) {
			t.Fatalf("称呼类事实应被拒绝，text=%q err=%v", text, err)
		}
	}
	if items, _ := tm.Working(ctx); len(items) != 0 {
		t.Fatalf("被拒绝的称呼不得落库，得到 %+v", items)
	}
}

// Test_F49_IdentityMarkerIsNarrow 确认判定词只认多字词，不误伤普通事实。
func Test_F49_IdentityMarkerIsNarrow(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"张三的昵称是大果篮",
		"他要求以后称呼自己为老师",
		"改名叫小明之后好记多了",
	}
	for _, text := range blocked {
		if !LooksLikeIdentityFact(text) {
			t.Fatalf("应判为称呼类事实: %q", text)
		}
	}
	allowed := []string{
		"张三很怕辣",
		"用户喜欢喝橙汁",
		"叫他早点睡", // 只有单字"叫"，不能误伤
		"通知：周五团建",
	}
	for _, text := range allowed {
		if LooksLikeIdentityFact(text) {
			t.Fatalf("不应判为称呼类事实: %q", text)
		}
	}
}

// Test_F49_SQLiteSupersedePersistsSubject 覆盖 SQLite 实现：就地改写与归属人
// 都要真的落库（含新增的 subject_id 列），而不是只在内存实现里成立。
func Test_F49_SQLiteSupersedePersistsSubject(t *testing.T) {
	t.Parallel()
	tiered, _ := newTieredOverSQLite(t, 50)
	ctx := scope.WithSubject(scope.WithScope(context.Background(), "g"), 7)

	if err := tiered.Save(ctx, "他住在梅河口，冬天很冷"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := tiered.Save(ctx, "他住在梅河口，冬天很冷。"); err != nil {
		t.Fatalf("Save(2): %v", err)
	}
	items, err := tiered.Working(ctx)
	if err != nil {
		t.Fatalf("Working: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("SQLite 上相似事实应就地改写，得到 %d 条: %+v", len(items), items)
	}
	if items[0].SubjectID != 7 {
		t.Fatalf("subject_id 应落库并读回，得到 %d", items[0].SubjectID)
	}
}
