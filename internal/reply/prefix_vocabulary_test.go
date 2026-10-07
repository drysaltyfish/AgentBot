package reply

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// Test_PrefixRelationVocabularyIsSharedAcrossPackages 钉住一处**跨包的字面量耦合**。
//
// 事实链条是这样接的：
//   - `store.compareDigest` 负责分类，并把结果作为**字符串字面量**写进
//     `prompt_snapshots.relation`（"identical" / "extended" / "slid" / "diverged"）；
//   - `reply` 拿到这个字符串，再拿它去比对 `llm` 的常量
//     （`llm.RelationDiverged`）来决定要不要报"前缀被意外改写"。
//
// 也就是说：store 产出的字符串与 llm 的常量**必须是同一套值**，
// 而两边是各自独立定义的——store 那四个根本没有导出常量，就是裸字面量。
// 任何一边改一个字母，`reply` 里那个 case 分支就再也匹配不上，
// 结果是"前缀分叉告警静默消失"：不报错、不掉测试，只是缓存要塌的时候没人知道。
//
// 这条测试用真实的 store 跑一遍分类，把两侧的值对起来。
func Test_PrefixRelationVocabularyIsSharedAcrossPackages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "relation.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// memoryDigest 全程不变：这样 store 不会走它特有的 memory_changed 分支，
	// 四个共享关系才可比较（memory_changed 是 ADR-0002 独有的，llm 侧没有对应值）。
	const mem = "mem-1"

	cases := []struct {
		name     string
		prev     []string
		next     []string
		wantLLM  string
		firstRun bool
	}{
		{name: "identical", prev: []string{"a", "b", "c"}, next: []string{"a", "b", "c"}, wantLLM: llm.RelationIdentical},
		{name: "extended", prev: []string{"a", "b", "c"}, next: []string{"a", "b", "c", "d"}, wantLLM: llm.RelationExtended},
		{name: "slid", prev: []string{"a", "b", "c"}, next: []string{"b", "c"}, wantLLM: llm.RelationSlid},
		{name: "diverged", prev: []string{"a", "b", "c"}, next: []string{"a", "X", "c"}, wantLLM: llm.RelationDiverged},
	}

	for i, tc := range cases {
		key := "relation-case-" + tc.name
		// 第一条快照没有前驱，关系由 store 自己决定；从第二条开始才是有意义的比较。
		if _, err := st.RecordPromptSnapshot(ctx, key, tc.prev, mem); err != nil {
			t.Fatalf("case %d (%s) 首条快照: %v", i, tc.name, err)
		}
		snap, err := st.RecordPromptSnapshot(ctx, key, tc.next, mem)
		if err != nil {
			t.Fatalf("case %d (%s) 第二条快照: %v", i, tc.name, err)
		}
		if snap.Relation != tc.wantLLM {
			t.Fatalf("%s: store 落库的 relation=%q，但 reply 会拿 llm.%s=%q 去比对——"+
				"两侧词表已经漂移，前缀分叉告警会静默失效",
				tc.name, snap.Relation, relationConstName(tc.name), tc.wantLLM)
		}
	}
}

// relationConstName 只为报错信息可读。
func relationConstName(name string) string {
	switch name {
	case "identical":
		return "RelationIdentical"
	case "extended":
		return "RelationExtended"
	case "slid":
		return "RelationSlid"
	case "diverged":
		return "RelationDiverged"
	default:
		return name
	}
}
