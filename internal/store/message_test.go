package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openMsgStore(t *testing.T) *Store {
	t.Helper()
	return openTest(t, Options{Path: filepath.Join(t.TempDir(), "msg.db")})
}

func addMsg(t *testing.T, s *Store, key, role, kind, content string) int64 {
	t.Helper()
	id, inserted, err := s.AppendMessage(context.Background(), Message{
		SessionKey: key, Role: role, Kind: kind, Content: content,
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if !inserted {
		t.Fatalf("应插入新消息: %q", content)
	}
	return id
}

func Test_F84_AppendAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	for i := 0; i < 5; i++ {
		addMsg(t, s, "k1", "user", "user", "内容")
	}
	msgs, err := s.Messages(context.Background(), "k1", 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("应有 5 条，实际 %d", len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Fatalf("seq 应连续: 第 %d 条 seq=%d", i, m.Seq)
		}
	}
}

// Test_F84_AppendWithExplicitSeqIsIdempotent 覆盖"导入两次结果与一次相同"。
func Test_F84_AppendWithExplicitSeqIsIdempotent(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	importOne := func(seq int64, content string) bool {
		_, inserted, err := s.AppendMessage(context.Background(), Message{
			SessionKey: "k", Seq: seq, Role: "user", Kind: "user", Content: content,
		})
		if err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
		return inserted
	}
	if !importOne(1, "第一条") || !importOne(2, "第二条") {
		t.Fatalf("首次导入应全部插入")
	}
	if importOne(1, "第一条") || importOne(2, "第二条") {
		t.Fatalf("重复导入必须跳过")
	}
	n, err := s.MessageCount(context.Background(), "k")
	if err != nil || n != 2 {
		t.Fatalf("重复导入后应仍为 2 条: n=%d err=%v", n, err)
	}
}

func Test_F84_MessagesLimitKeepsChronologicalOrder(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	for i := 1; i <= 10; i++ {
		addMsg(t, s, "k", "user", "user", string(rune('a'+i-1)))
	}
	msgs, err := s.Messages(context.Background(), "k", 3)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("应取 3 条，实际 %d", len(msgs))
	}
	if msgs[0].Seq != 8 || msgs[2].Seq != 10 {
		t.Fatalf("应取最近 3 条且保持升序: %d..%d", msgs[0].Seq, msgs[2].Seq)
	}
}

// Test_F84_SearchChineseLongQueryUsesFTS 覆盖 >=3 字的中文检索（trigram 生效）。
func Test_F84_SearchChineseLongQueryUsesFTS(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	addMsg(t, s, "k", "user", "user", "我喜欢喝橙汁")
	addMsg(t, s, "k", "assistant", "assistant", "记住啦，你喜欢橙汁")
	addMsg(t, s, "k", "user", "user", "今天天气不错")
	addMsg(t, s, "k", "user", "user", "完全无关的内容")

	hits, err := s.SearchMessages(context.Background(), "k", "喝橙汁", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("应命中 1 条，实际 %d: %+v", len(hits), hits)
	}
	if !strings.Contains(hits[0].Message.Content, "我喜欢喝橙汁") {
		t.Fatalf("命中了错误的行: %q", hits[0].Message.Content)
	}
	if !strings.Contains(hits[0].Snippet, "[") {
		t.Fatalf("应带片段高亮: %q", hits[0].Snippet)
	}
}

// Test_F84_SearchShortChineseFallsBackToLike 是本次最关键的用例。
//
// 实测 trigram 对短于 3 字符的查询命中为 0，而中文里两字词最常见。
// 回退 LIKE 后必须能搜到，否则中文检索就是坏的。
func Test_F84_SearchShortChineseFallsBackToLike(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	addMsg(t, s, "k", "user", "user", "我喜欢喝橙汁")
	addMsg(t, s, "k", "user", "user", "今天天气不错")
	addMsg(t, s, "k", "user", "user", "作业还没写完")

	for _, tc := range []struct{ q, want string }{
		{"橙汁", "橙汁"},
		{"天气", "天气"},
		{"作业", "作业"},
	} {
		hits, err := s.SearchMessages(context.Background(), "k", tc.q, 10)
		if err != nil {
			t.Fatalf("SearchMessages(%q): %v", tc.q, err)
		}
		if len(hits) != 1 || !strings.Contains(hits[0].Message.Content, tc.want) {
			t.Fatalf("两字查询 %q 必须命中: %+v", tc.q, hits)
		}
		if !strings.Contains(hits[0].Snippet, tc.want) {
			t.Fatalf("片段应包含命中词 %q: %q", tc.q, hits[0].Snippet)
		}
	}
}

// Test_F84_SearchHyphenQueryDoesNotError 覆盖实测过的报错：chat-send -> no such column: send。
func Test_F84_SearchHyphenQueryDoesNotError(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	addMsg(t, s, "k", "user", "user", "chat-send 失败了")
	addMsg(t, s, "k", "user", "user", "无关")

	weird := []string{"chat-send", "foo@DQ@ OR", "a AND", "send", "(", "*", "100%", "@DQ@@DQ@"}
	for _, q := range weird {
		q = strings.ReplaceAll(q, "@DQ@", string(rune(34)))
		if _, err := s.SearchMessages(context.Background(), "k", q, 10); err != nil {
			t.Fatalf("畸形查询 %q 不应报错: %v", q, err)
		}
	}
	hits, err := s.SearchMessages(context.Background(), "k", "chat-send", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("chat-send 应命中 1 条: %+v", hits)
	}
}

// Test_F84_SearchIsScopedToSession 覆盖跨会话隔离。
func Test_F84_SearchIsScopedToSession(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	addMsg(t, s, "group-a", "user", "user", "群 A 说橙汁好喝")
	addMsg(t, s, "group-b", "user", "user", "群 B 也在说橙汁")

	hits, err := s.SearchMessages(context.Background(), "group-a", "橙汁", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Message.Content, "群 A") {
		t.Fatalf("不得跨会话召回: %+v", hits)
	}
}

// Test_F84_DeleteRemovesFromFTS 覆盖"删掉就不再命中"。
func Test_F84_DeleteRemovesFromFTS(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	id := addMsg(t, s, "k", "user", "user", "要被删掉的橙汁内容")

	hits, _ := s.SearchMessages(context.Background(), "k", "橙汁", 10)
	if len(hits) != 1 {
		t.Fatalf("删除前应命中: %+v", hits)
	}
	if err := s.DeleteMessage(context.Background(), id); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	hits, _ = s.SearchMessages(context.Background(), "k", "橙汁", 10)
	if len(hits) != 0 {
		t.Fatalf("删除后不应命中: %+v", hits)
	}
	if err := s.DeleteMessage(context.Background(), id); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("重复删除应返回 ErrMessageNotFound: %v", err)
	}
}

func Test_F84_SearchEmptyReturnsRecent(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	for i := 1; i <= 5; i++ {
		addMsg(t, s, "k", "user", "user", "第"+string(rune('0'+i))+"条")
	}
	hits, err := s.SearchMessages(context.Background(), "k", "   ", 2)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("空查询应返回最近 2 条: %d", len(hits))
	}
	if hits[0].Snippet != "" {
		t.Fatalf("空查询无关键词，不该有片段: %q", hits[0].Snippet)
	}
}

func Test_F84_NeighboursAreAttached(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	addMsg(t, s, "k", "user", "user", "前一条")
	addMsg(t, s, "k", "user", "user", "中间提到橙汁")
	addMsg(t, s, "k", "assistant", "assistant", "后一条")

	hits, err := s.SearchMessages(context.Background(), "k", "橙汁", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("应命中 1 条: %d", len(hits))
	}
	h := hits[0]
	if h.Before == nil || h.Before.Content != "前一条" {
		t.Fatalf("应带前一条: %+v", h.Before)
	}
	if h.After == nil || h.After.Content != "后一条" {
		t.Fatalf("应带后一条: %+v", h.After)
	}
}

func Test_F84_TrimKeepsNewest(t *testing.T) {
	t.Parallel()
	s := openMsgStore(t)
	for i := 1; i <= 10; i++ {
		addMsg(t, s, "k", "user", "user", "m"+string(rune('0'+i%10)))
	}
	removed, err := s.TrimMessages(context.Background(), "k", 4)
	if err != nil {
		t.Fatalf("TrimMessages: %v", err)
	}
	if removed != 6 {
		t.Fatalf("应删除 6 条，实际 %d", removed)
	}
	msgs, _ := s.Messages(context.Background(), "k", 0)
	if len(msgs) != 4 || msgs[0].Seq != 7 {
		t.Fatalf("应保留最近 4 条: %d 条, 首条 seq=%d", len(msgs), msgs[0].Seq)
	}
}
