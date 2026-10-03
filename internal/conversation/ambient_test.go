package conversation

import (
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/history"
)

func amb(content, speaker string) history.Item {
	return history.Item{Kind: history.KindUser, Content: content, SpeakerName: speaker, Ambient: true}
}

func convo(content string) history.Item {
	return history.Item{Kind: history.KindUser, Content: content}
}

// Test_Ambient_TokenBudgetKeepsNewest 覆盖"按 token 预算而不是条数保留"。
func Test_Ambient_TokenBudgetKeepsNewest(t *testing.T) {
	t.Parallel()
	var items []history.Item
	for i := 0; i < 50; i++ {
		items = append(items, amb(strings.Repeat("字", 40), "小明"))
	}
	// 预算 200 token：中文 40 字约 40 token，含说话人标签后约 45，
	// 因此只应保留最近几条。
	kept, dropped := CompressAmbient(items, AmbientOptions{TokenBudget: 200, MaxCharsPerMessage: 200})
	if dropped == 0 {
		t.Fatalf("预算远小于总量时必须丢东西: dropped=%d", dropped)
	}
	if len(kept) == 0 || len(kept) >= len(items) {
		t.Fatalf("应保留一部分且不是全部: kept=%d", len(kept))
	}
	if kept[0].Content != items[len(items)-len(kept)].Content {
		t.Fatalf("保留的必须是**最近**的若干条")
	}
}

// Test_Ambient_AtLeastOneSurvives 守住"预算再紧也留一条"。
func Test_Ambient_AtLeastOneSurvives(t *testing.T) {
	t.Parallel()
	items := []history.Item{amb(strings.Repeat("字", 5000), "小明")}
	kept, _ := CompressAmbient(items, AmbientOptions{TokenBudget: 10, MaxCharsPerMessage: 200})
	if len(kept) != 1 {
		t.Fatalf("至少保留一条，让模型知道刚才有人说过话: %d", len(kept))
	}
}

// Test_Ambient_LongMessageTruncatedWithHint 覆盖"超长省略并提示可回溯"。
func Test_Ambient_LongMessageTruncatedWithHint(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("很长的内容", 100) // 500 字
	kept, _ := CompressAmbient([]history.Item{amb(long, "小明")}, AmbientOptions{TokenBudget: 0, MaxCharsPerMessage: 50})
	if len(kept) != 1 {
		t.Fatalf("应保留一条: %d", len(kept))
	}
	if strings.Contains(kept[0].Content, "很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容很长的内容") {
		t.Fatalf("超长内容应被截断")
	}
	if !strings.Contains(kept[0].Content, "recall_history") {
		t.Fatalf("必须提示可用 recall_history 看完整内容: %q", kept[0].Content)
	}
	// 短消息不该被截断。
	short, _ := CompressAmbient([]history.Item{amb("短的", "小明")}, AmbientOptions{MaxCharsPerMessage: 50})
	if short[0].Content != "短的" {
		t.Fatalf("短消息不应改动: %q", short[0].Content)
	}
}

// Test_Ambient_AdjacentIdenticalMerged 覆盖"多人刷屏只留一份并合并"。
func Test_Ambient_AdjacentIdenticalMerged(t *testing.T) {
	t.Parallel()
	items := []history.Item{
		amb("哈哈哈哈", "小明"),
		amb("哈哈哈哈", "小红"),
		amb("哈哈哈哈", "小刚"),
		amb("说点别的", "小明"),
	}
	kept, _ := CompressAmbient(items, AmbientOptions{})
	if len(kept) != 2 {
		t.Fatalf("三条相同应合成一条: %+v", kept)
	}
	if kept[0].Content != "哈哈哈哈" {
		t.Fatalf("内容应保留一份: %q", kept[0].Content)
	}
	if !strings.Contains(kept[0].SpeakerName, "3") {
		t.Fatalf("应标出重复次数: %q", kept[0].SpeakerName)
	}
	if kept[0].SpeakerID != 0 {
		t.Fatalf("合并后不再是某一个人说的，speaker 应清空")
	}
	if kept[1].Content != "说点别的" {
		t.Fatalf("不同的内容不该被合并: %+v", kept[1])
	}
}

// Test_Ambient_SameContentNonAdjacentIsNotMerged 守住"只合并连续"。
func Test_Ambient_SameContentNonAdjacentIsNotMerged(t *testing.T) {
	t.Parallel()
	items := []history.Item{
		amb("哈哈", "小明"),
		amb("插一句", "小红"),
		amb("哈哈", "小刚"),
	}
	kept, _ := CompressAmbient(items, AmbientOptions{})
	if len(kept) != 3 {
		t.Fatalf("不连续的相同内容不该合并: %+v", kept)
	}
}

// Test_Assembler_SeparatesConvoAndAmbient 是本次改动的核心：两类消息各裁各的。
func Test_Assembler_SeparatesConvoAndAmbient(t *testing.T) {
	t.Parallel()
	a := New(Options{System: "S", MaxHistory: 2, AmbientTokenBudget: 40, AmbientMaxChars: 100})

	var items []history.Item
	// 大量环境消息 + 少量对话消息。
	for i := 0; i < 30; i++ {
		items = append(items, amb(strings.Repeat("刷", 20), "群友"))
	}
	items = append(items, convo("我问的第一句"))
	items = append(items, convo("我问的第二句"))

	msgs := a.Build(items, "现在这句")
	joined := ""
	for _, m := range msgs {
		joined += m.Content + "|"
	}
	// **对话必须完整保留**——这正是与"全部按条数裁"的关键区别。
	if !strings.Contains(joined, "我问的第一句") || !strings.Contains(joined, "我问的第二句") {
		t.Fatalf("被 @ 的对话不该被环境消息挤掉: %q", joined)
	}
	if !strings.Contains(joined, "现在这句") {
		t.Fatalf("当前输入必须在: %q", joined)
	}
	// 环境消息要被压到预算内（30 条 × 20 字远超 40 token 预算）。
	if n := strings.Count(joined, strings.Repeat("刷", 20)); n > 3 {
		t.Fatalf("环境消息应被压到预算内，实际保留 %d 条", n)
	}
}
