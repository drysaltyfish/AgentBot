package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/llm"
)

// ReflectOptions 是记忆反思的配置（F-48 的延伸）。
//
// 反思会在用户没说话时**主动发起模型调用**，因此每一步都以省钱为先：
// 静默期 + 滑出窗口 + 节流 + 每日预算，四个条件缺一不可。
type ReflectOptions struct {
	// Model 是**关闭思考**的反思客户端；nil 表示不可用（反思整体跳过）。
	Model llm.LLM
	// ModelName 仅用于日志与台账。
	ModelName string
	// Timeout 是单次反思的预算；<=0 时用 DefaultReflectTimeout。
	Timeout time.Duration
	// MaxItems 是单次送进去的最大条目数；<=0 时用 DefaultReflectMaxItems。
	MaxItems int
	// MaxFacts 是单次最多写入的事实数；<=0 时用 DefaultReflectMaxFacts。
	MaxFacts int
	// DailyBudget 是每日反思调用上限；<=0 时用 DefaultReflectDailyBudget。
	// 这是花钱的硬闸门：宁可漏反思，也不让后台任务把额度烧掉。
	DailyBudget int
	// MaxFactLen 是单条事实的字符上限；<=0 时用 DefaultReflectFactLen。
	MaxFactLen int
	// Warn 接收降级告警。
	Warn func(string)
	// Now 注入时间源（测试用）。
	Now func() time.Time
}

// 反思的默认参数。取值都偏保守：这是后台开销，宁可少做。
const (
	// DefaultReflectTimeout 是单次反思的总预算。
	DefaultReflectTimeout = 45 * time.Second
	// DefaultReflectMaxItems 是单次最多送进去的条目数。
	DefaultReflectMaxItems = 40
	// DefaultReflectMaxFacts 是单次最多写入的事实数。
	DefaultReflectMaxFacts = 5
	// DefaultReflectDailyBudget 是每日反思调用上限。
	DefaultReflectDailyBudget = 50
	// DefaultReflectFactLen 是单条事实的字符上限。
	DefaultReflectFactLen = 60
)

const reflectSystemPrompt = "你是一个记忆整理器。下面是一段群聊记录，其中的内容已经滑出了机器人的上下文窗口。" +
	"你的任务：提取**值得长期记住**的事实，丢弃寒暄、玩笑、临时状态与无信息量的内容。" +
	"输出严格的 JSON 数组，不要任何解释、不要代码块标记。每个元素形如：" +
	"{\"fact\":\"张三很怕辣\",\"shared\":false}。" +
	"判断规则：" +
	"1) 关于某个具体人的（喜好、习惯、承诺、长期经历）→ shared 为 false，" +
	"并在 fact 里用昵称或 QQ 号写明是谁；" +
	"不要输出对方叫什么：昵称、姓名、外号、称呼这类内容不是事实，是平台名片，" +
	"写进记忆只会留下一份会过期的副本；" +
	"2) 属于大家共同的事（活动安排、群规、共同决定、公共通知）→ shared 为 true，" +
	"fact 里不要写具体某人；" +
	"3) 没有值得记住的内容时输出 []。只输出 JSON。"

// ReflectedFact 是一条反思产出。
type ReflectedFact struct {
	// Fact 是事实正文（已裁剪、单行）。
	Fact string `json:"fact"`
	// Shared 为 true 表示公共记忆，不属于任何个人。
	Shared bool `json:"shared"`
}

// Reflector 对"已滑出窗口的历史"做一次反思并写入记忆。
//
// 它是**无状态**的：水位（哪些内容已反思过）由调用方持有并传进来，
// 这样预算计数与水位都能落在组合根，便于观测与测试。
type Reflector struct {
	opts ReflectOptions

	mu       sync.Mutex
	usedDay  string // 预算所属日期（本地日）
	usedCall int    // 当日已用次数
}

// NewReflector 构造反思器。
func NewReflector(opts ReflectOptions) *Reflector {
	return &Reflector{opts: opts}
}

func (r *Reflector) timeout() time.Duration {
	if r.opts.Timeout > 0 {
		return r.opts.Timeout
	}
	return DefaultReflectTimeout
}

func (r *Reflector) maxItems() int {
	if r.opts.MaxItems > 0 {
		return r.opts.MaxItems
	}
	return DefaultReflectMaxItems
}

func (r *Reflector) maxFacts() int {
	if r.opts.MaxFacts > 0 {
		return r.opts.MaxFacts
	}
	return DefaultReflectMaxFacts
}

func (r *Reflector) dailyBudget() int {
	if r.opts.DailyBudget > 0 {
		return r.opts.DailyBudget
	}
	return DefaultReflectDailyBudget
}

func (r *Reflector) maxFactLen() int {
	if r.opts.MaxFactLen > 0 {
		return r.opts.MaxFactLen
	}
	return DefaultReflectFactLen
}

func (r *Reflector) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}

func (r *Reflector) warn(msg string) {
	if r.opts.Warn != nil {
		r.opts.Warn(msg)
	}
}

// BudgetLeft 返回今日剩余的反思调用次数。
func (r *Reflector) BudgetLeft() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollDayLocked()
	return r.dailyBudget() - r.usedCall
}

// rollDayLocked 跨天时重置计数。
func (r *Reflector) rollDayLocked() {
	day := r.now().Format("2006-01-02")
	if r.usedDay != day {
		r.usedDay = day
		r.usedCall = 0
	}
}

// reserve 预占一次调用额度；额度用尽返回 false（**不发起调用**）。
//
// 先占额度再调用，而不是调用完再记账：失败的重试也想花钱，
// 用"先占"才能保证硬上限真的硬。
func (r *Reflector) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollDayLocked()
	if r.usedCall >= r.dailyBudget() {
		return false
	}
	r.usedCall++
	return true
}

// Reflect 对一段已滑出窗口的记录做反思，返回解析出的事实。
//
// entries 是**已经压缩过**的文本行（调用方负责合并环境消息、截断超长条目）——
// 压缩放在调用方是因为它依赖会话的历史结构，而反思器只关心"一段文本"。
func (r *Reflector) Reflect(ctx context.Context, entries []string) ([]ReflectedFact, error) {
	if r == nil || r.opts.Model == nil {
		return nil, ErrUnavailable
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		if s := strings.TrimSpace(e); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	if len(lines) > r.maxItems() {
		// 只取**最近**的若干条：越新的越可能与当前相关。
		lines = lines[len(lines)-r.maxItems():]
	}
	if !r.reserve() {
		r.warn("memory reflection skipped: daily budget exhausted")
		return nil, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	resp, err := r.opts.Model.Chat(callCtx, &llm.ChatRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: reflectSystemPrompt},
		{Role: llm.RoleUser, Content: strings.Join(lines, "\n")},
	}})
	if err != nil {
		return nil, fmt.Errorf("reflect call: %w", err)
	}
	facts, err := ParseFacts(resp.Content, r.maxFacts(), r.maxFactLen())
	if err != nil {
		return nil, err
	}
	return facts, nil
}

// ParseFacts 解析反思输出。
//
// 容忍模型写成代码块或前后带解释：只截取第一个 '[' 到最后一个 ']'。
// 但**不接受**非 JSON 的自由文本——那条路一旦放开，写入的就是无法解释的噪声。
func ParseFacts(raw string, maxFacts, maxLen int) ([]ReflectedFact, error) {
	text := strings.TrimSpace(raw)
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil, ErrReflectUnparsed
	}
	var facts []ReflectedFact
	if err := json.Unmarshal([]byte(text[start:end+1]), &facts); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReflectUnparsed, err)
	}

	out := make([]ReflectedFact, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		fact := strings.Join(strings.Fields(f.Fact), " ") // 压平换行与多余空白
		if fact == "" {
			continue
		}
		if maxLen > 0 && len([]rune(fact)) > maxLen {
			fact = string([]rune(fact)[:maxLen])
		}
		if _, dup := seen[fact]; dup {
			continue
		}
		seen[fact] = struct{}{}
		out = append(out, ReflectedFact{Fact: fact, Shared: f.Shared})
		if maxFacts > 0 && len(out) >= maxFacts {
			break
		}
	}
	return out, nil
}
