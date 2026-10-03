package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
	"github.com/drysaltyfish/agentbot/internal/store"
)

// Judge 判定两条文本是否指同一件事。
//
// 抽象成接口是为了让"是否引入模型判定"成为可替换的决定，也便于测试注入确定性实现。
type Judge interface {
	SameFact(ctx context.Context, a, b string) (bool, error)
}

// DefaultJudgeTimeout 是单次判定的时间预算。
//
// 取得较短：判官只是把判定做得更准，不值得让一次记忆写入等太久。
const DefaultJudgeTimeout = 5 * time.Second

// judgeSystemPrompt 要求模型只回答是或否。
//
// 明确"只回答一个字"是必要的：开了思考的模型会输出大段推理，
// 而这里只需要一个标签。配套模型应当**关闭思考**（见 main 的判官客户端）。
const judgeSystemPrompt = "你是一个判定器。判断下面两条关于同一个人的记录是不是**同一件事**。" +
	"只允许回答一个字：是 或 否。不要解释，不要标点。"

// LLMJudge 用一次不开思考的模型调用做语义判定。
//
// 判定结果按**文本对**缓存：同一对永不重复询问。这既省一次调用，
// 也保证同一个输入永远得到同一个结论——记忆内容会进入提示词前缀，
// 结论漂移会让前缀无谓地变化（ADR-0002）。
type LLMJudge struct {
	llm     llm.LLM
	timeout time.Duration
	warn    func(string)

	mu    sync.Mutex
	cache map[string]bool
}

// NewLLMJudge 构造；model 应当是**关闭思考**的客户端。
func NewLLMJudge(model llm.LLM, warn func(string)) *LLMJudge {
	return &LLMJudge{llm: model, timeout: DefaultJudgeTimeout, warn: warn, cache: map[string]bool{}}
}

// SameFact 实现 Judge。
func (j *LLMJudge) SameFact(ctx context.Context, a, b string) (bool, error) {
	if j == nil || j.llm == nil {
		return false, ErrUnavailable
	}
	key := judgeKey(a, b)
	if v, ok := j.lookup(key); ok {
		return v, nil
	}

	timeout := j.timeout
	if timeout <= 0 {
		timeout = DefaultJudgeTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := j.llm.Chat(callCtx, &llm.ChatRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: judgeSystemPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("1. %s\n2. %s", a, b)},
	}})
	if err != nil {
		return false, fmt.Errorf("judge call: %w", err)
	}
	verdict, err := ParseVerdict(resp.Content)
	if err != nil {
		return false, err
	}
	j.store(key, verdict)
	return verdict, nil
}

// ParseVerdict 把判官的回答解析成布尔。
//
// 先查否定前缀再查肯定前缀：否则"不一样"会被"一样"抢先匹配成肯定。
func ParseVerdict(s string) (bool, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return false, ErrJudgeUnparsed
	}
	for _, neg := range []string{"不是", "不一样", "不同", "否", "不", "no", "No", "NO", "false"} {
		if strings.HasPrefix(t, neg) {
			return false, nil
		}
	}
	for _, pos := range []string{"是", "对", "一样", "同一", "yes", "Yes", "YES", "true"} {
		if strings.HasPrefix(t, pos) {
			return true, nil
		}
	}
	return false, ErrJudgeUnparsed
}

// judgeKey 生成与顺序无关的缓存键。
func judgeKey(a, b string) string {
	x, y := strings.TrimSpace(a), strings.TrimSpace(b)
	if x > y {
		x, y = y, x
	}
	return store.Fingerprint("judge", x, y)
}

func (j *LLMJudge) lookup(key string) (bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	v, ok := j.cache[key]
	return v, ok
}

func (j *LLMJudge) store(key string, v bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	// 缓存上界：判定条目很少，但仍设一个上限避免无界增长。
	const maxEntries = 4096
	if len(j.cache) >= maxEntries {
		for k := range j.cache {
			delete(j.cache, k)
			break
		}
	}
	j.cache[key] = v
}

// CacheLen 返回缓存条目数（测试与观测用）。
func (j *LLMJudge) CacheLen() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.cache)
}
