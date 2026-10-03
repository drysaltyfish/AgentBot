package memory

import (
	"context"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/scope"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/textsim"
)

// 判定分带：相似度落在 [LowBand, HighBand) 之外时不需要语义判断。
const (
	// LowBand 以下直接判为"不同的事"。
	LowBand = 0.30
	// HighBand 以上直接判为"同一件事"。
	HighBand = 0.90
)

// Options 是构造 Store 的参数。
type Options struct {
	// Store 是持久层，必填。
	Store *store.Store
	// Judge 是可选的语义判官；为 nil 时歧义带按确定性阈值处理。
	Judge Judge
	// DeterministicThreshold <= 0 时用 textsim.Threshold。
	DeterministicThreshold float64
	// MaxPerScope <= 0 时不限制；否则每次写入后淘汰到该条数（F-48 的每作用域 200 条）。
	MaxPerScope int
	// Warn 接收降级告警。
	Warn func(string)
}

// Store 是基于持久层的记忆实现（F-87）。
//
// 写入决策是**混合**的：
//
//	① 指纹完全相同          -> 忽略（确定性，且幂等）
//	② 相似度 >= HighBand    -> 并入（确定性）
//	③ 相似度 <  LowBand     -> 新增（确定性）
//	④ 落在歧义带            -> 问 Judge；Judge 失败则退回确定性阈值
//
// 分带的意义在于**大多数写入不付额外调用**：字符相似度足以处理明显的情况，
// 只有真正含糊的那一小撮才值得问一次模型。
type Store struct {
	st          *store.Store
	judge       Judge
	threshold   float64
	warn        func(string)
	maxPerScope int

	// 观测计数：判定走了哪条路。
	judgedFallbacks int
}

// New 构造。
func New(opts Options) *Store {
	threshold := opts.DeterministicThreshold
	if threshold <= 0 {
		threshold = textsim.Threshold
	}
	return &Store{
		st: opts.Store, judge: opts.Judge, threshold: threshold,
		warn: opts.Warn, maxPerScope: opts.MaxPerScope,
	}
}

// Save 实现 agent.Memory：把一条事实写入当前作用域。
//
// 作用域经 ctx 传递（键由 internal/scope 拥有，agent.WithMemoryScope 是它的别名），
// 因此这里不接收 scope 参数。
func (s *Store) Save(ctx context.Context, text string) error {
	if s == nil || s.st == nil {
		return ErrUnavailable
	}
	trimmed, err := Validate(text)
	if err != nil {
		return err
	}
	scopeKey := scope.ScopeFrom(ctx)
	if scopeKey == "" {
		// 空作用域会把所有会话的记忆混在一起——宁可写入失败，也不能串。
		return fmt.Errorf("save memory: %w", ErrUnavailable)
	}

	best, sim, found, err := s.st.FindSimilarMemory(ctx, scopeKey, trimmed)
	if err != nil {
		return err
	}

	var opts store.MemoryWriteOptions
	if found {
		switch {
		case sim >= HighBand:
			opts.ForceMergeID = best.ID
		case sim < LowBand:
			opts.ForceAdd = true
		default:
			s.decideAmbiguous(ctx, trimmed, best.Text, sim, best.ID, &opts)
		}
	}

	res, err := s.st.SaveMemoryWith(ctx, store.Memory{ScopeKey: scopeKey, Text: trimmed}, opts)
	if err != nil {
		return err
	}
	if s.warn != nil && res.Decision != store.MemoryIgnored {
		s.warn(fmt.Sprintf("memory %s: %s", res.Decision, res.Reason))
	}
	// 留存：写入后顺手淘汰，不让作用域无界增长。
	if s.maxPerScope > 0 {
		if n, terr := s.st.TrimMemories(ctx, scopeKey, s.maxPerScope); terr != nil {
			// 淘汰失败不影响这次写入的成功语义，但必须留痕。
			if s.warn != nil {
				s.warn("memory retention trim failed: " + terr.Error())
			}
		} else if n > 0 && s.warn != nil {
			s.warn(fmt.Sprintf("memory retention evicted %d entr(ies) in scope %s", n, scopeKey))
		}
	}
	return nil
}

// decideAmbiguous 处理歧义带：优先问判官，失败则退回确定性阈值（用户选定的语义）。
func (s *Store) decideAmbiguous(ctx context.Context, text, existing string, sim float64, id int64, opts *store.MemoryWriteOptions) {
	if s.judge != nil {
		same, err := s.judge.SameFact(ctx, text, existing)
		switch {
		case err == nil && same:
			opts.ForceMergeID = id
			return
		case err == nil:
			opts.ForceAdd = true
			return
		default:
			// 判官不可用：**不能因此丢记忆**，也不能因此拒绝写入。
			// 退回确定性判据，并留下痕迹。
			s.judgedFallbacks++
			if s.warn != nil {
				s.warn("memory judge unavailable; falling back to the deterministic threshold: " + err.Error())
			}
		}
	}
	// 确定性兜底（也是 judge == nil 时的正常路径）。
	if sim >= s.threshold {
		opts.ForceMergeID = id
		return
	}
	opts.ForceAdd = true
}

// Recall 实现 agent.Memory：返回当前作用域的全部记忆，顺序确定。
func (s *Store) Recall(ctx context.Context) ([]string, error) {
	if s == nil || s.st == nil {
		return nil, ErrUnavailable
	}
	items, err := s.st.RecallMemories(ctx, scope.ScopeFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, m := range items {
		out = append(out, m.Text)
	}
	return out, nil
}

// List 按更新时间倒序返回当前作用域的记忆（供用户检视）。
func (s *Store) List(ctx context.Context, limit int) ([]store.Memory, error) {
	if s == nil || s.st == nil {
		return nil, ErrUnavailable
	}
	return s.st.ListMemories(ctx, scope.ScopeFrom(ctx), limit)
}

// Forget 删除一条记忆（幂等）。
func (s *Store) Forget(ctx context.Context, id int64) (bool, error) {
	if s == nil || s.st == nil {
		return false, ErrUnavailable
	}
	return s.st.ForgetMemory(ctx, scope.ScopeFrom(ctx), id)
}

// ForgetScope 清空当前作用域。
func (s *Store) ForgetScope(ctx context.Context) (int, error) {
	if s == nil || s.st == nil {
		return 0, ErrUnavailable
	}
	return s.st.ForgetScope(ctx, scope.ScopeFrom(ctx))
}

// Trim 把当前作用域淘汰到 keep 条（F-88 的留存策略）。
func (s *Store) Trim(ctx context.Context, keep int) (int, error) {
	if s == nil || s.st == nil {
		return 0, ErrUnavailable
	}
	return s.st.TrimMemories(ctx, scope.ScopeFrom(ctx), keep)
}

// JudgeFallbacks 返回因判官失败而退回确定性判据的次数（观测用）。
func (s *Store) JudgeFallbacks() int { return s.judgedFallbacks }
