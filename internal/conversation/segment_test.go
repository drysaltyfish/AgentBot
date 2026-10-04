package conversation

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/history"
	"github.com/drysaltyfish/agentbot/internal/session"
)

// Test_F65_StaticSegmentIsByteStableAcrossDynamicInputs 是 F-65 的第一条验收：
// 连续 100 次渲染、每次都换时间/历史/输入，静态段哈希必须不变。
func Test_F65_StaticSegmentIsByteStableAcrossDynamicInputs(t *testing.T) {
	t.Parallel()

	asm := New(Options{
		System: "静态前缀：系统指令 + 工具 schema + 权限表",
		HalfStatic: func(context.Context, session.Key) string {
			return "人格设定（小时级变化）"
		},
	})
	want := HashText(asm.Prefix())
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		key := session.Key{GroupID: int64(i), UserID: int64(i * 3)}
		hist := []history.Item{user("第 " + strconv.Itoa(i) + " 轮"), asst("回答 " + strconv.Itoa(i))}
		msgs := asm.BuildFor(ctx, key, hist, "记忆 "+strconv.Itoa(i), "输入 "+strconv.Itoa(i))
		if len(msgs) == 0 {
			t.Fatal("empty message sequence")
		}
		if !strings.HasPrefix(msgs[0].Content, asm.Prefix()) {
			t.Fatalf("第 %d 次：system 消息没有以静态段开头: %q", i, msgs[0].Content)
		}
		if got := asm.PrefixHash(); got != want {
			t.Fatalf("第 %d 次：静态段哈希变了: %s -> %s", i, want, got)
		}
		segs := asm.Segments(ctx, key, "记忆 "+strconv.Itoa(i))
		if segs[0].Hash != want {
			t.Fatalf("第 %d 次：报告里的静态段哈希不稳: %s", i, segs[0].Hash)
		}
	}
}

// Test_F65_HalfStaticTracksPersonaNotScope 钉住两件事：
//   - 换人格必须改变半静态段哈希（F-82 的验收）；
//   - 同人格的不同会话必须得到同一段半静态文本，否则跨会话的前缀共享就没了。
func Test_F65_HalfStaticTracksPersonaNotScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	persona := func(text string) func(context.Context, session.Key) string {
		return func(context.Context, session.Key) string { return text }
	}
	a := New(Options{System: "S", HalfStatic: persona("人格 A")})
	b := New(Options{System: "S", HalfStatic: persona("人格 B")})
	keyA := session.Key{GroupID: 1, UserID: 2}
	keyB := session.Key{GroupID: 9, UserID: 99}

	sa := a.Segments(ctx, keyA, "")
	sb := b.Segments(ctx, keyA, "")
	if sa[0].Hash != sb[0].Hash {
		t.Fatalf("静态段哈希不该随人格变化: %s vs %s", sa[0].Hash, sb[0].Hash)
	}
	if sa[1].Hash == sb[1].Hash {
		t.Fatal("半静态段哈希必须随人格变化")
	}
	if other := a.Segments(ctx, keyB, "")[1].Hash; other != sa[1].Hash {
		t.Fatalf("同一人格的不同会话必须得到同一半静态段: %s vs %s", other, sa[1].Hash)
	}
	if len(sa) != 3 || sa[2].Name != "dynamic" {
		t.Fatalf("段报告必须固定为 static/half-static/dynamic 三段: %+v", sa)
	}
}

// Test_F65_HalfStaticIncludesMemory 说明长期记忆与人格同属半静态频率类：
// 记忆一变，半静态段哈希必须变（那是 ADR-0002 接受的代价，不是异常）。
func Test_F65_HalfStaticIncludesMemory(t *testing.T) {
	t.Parallel()

	asm := New(Options{System: "S", HalfStatic: func(context.Context, session.Key) string { return "人格" }})
	ctx := context.Background()
	key := session.Key{GroupID: 1, UserID: 2}
	if asm.Segments(ctx, key, "")[1].Hash == asm.Segments(ctx, key, "一条记忆")[1].Hash {
		t.Fatal("记忆变化必须改变半静态段哈希")
	}
}

// Test_F65_AbsentHalfStaticKeepsSystemByteIdentical 是回归护栏：
// 没配置人格时，system 消息必须逐字节等于旧行为（静态段本身）。
func Test_F65_AbsentHalfStaticKeepsSystemByteIdentical(t *testing.T) {
	t.Parallel()

	asm := New(Options{System: "S"})
	msgs := asm.BuildFor(context.Background(), session.Key{}, nil, "", "hi")
	if len(msgs) == 0 || msgs[0].Content != "S" {
		t.Fatalf("无半静态段时 system 必须逐字节等于静态段: %+v", msgs)
	}
	if isEmpty := asm.Segments(context.Background(), session.Key{}, "")[1].Hash; isEmpty == "" {
		t.Fatal("半静态段为空也应有稳定哈希")
	}
}

// Test_F65_StaticSegmentFirstWhenMemoryPresent 固定三段物理顺序（F-65 + ADR-0002）：
// [静态+半静态 system] → [记忆 system] → [历史] → [当前输入]。
func Test_F65_StaticSegmentFirstWhenMemoryPresent(t *testing.T) {
	t.Parallel()

	asm := New(Options{System: "静态", HalfStatic: func(context.Context, session.Key) string { return "人格" }})
	msgs := asm.BuildFor(context.Background(), session.Key{}, []history.Item{user("历史")}, "记忆块", "现在")
	if len(msgs) != 4 {
		t.Fatalf("消息数=%d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "静态"+"\n\n"+"人格" {
		t.Fatalf("system 应由静态段与半静态段按序拼接: %q", msgs[0].Content)
	}
	if msgs[1].Content != "记忆块" {
		t.Fatalf("记忆必须紧随 system 之后（ADR-0002）: %q", msgs[1].Content)
	}
	if msgs[3].Content != "现在" {
		t.Fatalf("当前输入必须在最后: %q", msgs[3].Content)
	}
}

// Test_F32_AssemblerMarksSystemPinned 钉住 F-32 的前置条件：
// 预算裁剪只尊重 Pinned 标记，而装配器是消息序列的唯一生产者——
// 它不标 Pinned，"system 永不被裁掉"这条边界就无从成立。
func Test_F32_AssemblerMarksSystemPinned(t *testing.T) {
	t.Parallel()
	asm := New(Options{System: "静态段"})
	msgs := asm.BuildFor(context.Background(), session.Key{}, []history.Item{user("你好")}, "记忆块", "现在")
	if len(msgs) < 2 {
		t.Fatalf("消息数=%d", len(msgs))
	}
	if !msgs[0].Pinned || msgs[0].Content != "静态段" {
		t.Fatalf("system 消息必须 Pinned 且内容正确: %+v", msgs[0])
	}
	// 记忆块可以不在裁剪中幸存（它是 ADR-0002 之后的位置），但不应被误标。
	if msgs[1].Pinned {
		t.Fatalf("记忆块不应被标为 Pinned: %+v", msgs[1])
	}
}
