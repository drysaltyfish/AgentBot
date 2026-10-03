package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/store"
)

// Hit 是一条检索命中：条目本体 + 命中片段 + 前后各一条。
type Hit struct {
	Item    Item
	Snippet string
	Before  *Item
	After   *Item
}

// Searcher 是历史存储的可选能力：按关键词检索。
//
// 独立于 History：能存不等于能搜，只有支持检索的实现才提供它。
// F-84 起 recall_history 优先走这里，退化为"读全量再过滤"的老路只在没有检索能力时发生。
type Searcher interface {
	Search(ctx context.Context, key, query string, limit int) ([]Hit, error)
}

// SQLite 是基于持久层的历史实现（F-84）。
type SQLite struct {
	st      *store.Store
	max     int
	trimmer Trimmer
}

// NewSQLite 构造；maxItems <= 0 时用 50。
func NewSQLite(st *store.Store, maxItems int) *SQLite {
	if maxItems <= 0 {
		maxItems = 50
	}
	return &SQLite{st: st, max: maxItems, trimmer: HighWater{Max: maxItems, Low: maxItems * 3 / 4}}
}

// WithTrimmer 替换裁剪策略。
func (h *SQLite) WithTrimmer(t Trimmer) *SQLite {
	if t != nil {
		h.trimmer = t
	}
	return h
}

// Append 追加一条条目，并在超出上限时按策略裁剪。
func (h *SQLite) Append(ctx context.Context, key string, item Item) error {
	m := store.Message{
		SessionKey: key,
		Role:       roleOf(item.Kind),
		Kind:       string(item.Kind),
		Content:    item.Content,
		Name:       item.Name,
		ToolCallID: item.ToolCallID,
		CreatedAt:  atMillis(item.At),
	}
	if len(item.ToolCalls) > 0 {
		raw, err := json.Marshal(item.ToolCalls)
		if err != nil {
			return fmt.Errorf("encode tool calls: %w", err)
		}
		m.ToolCalls = string(raw)
	}
	if _, _, err := h.st.AppendMessage(ctx, m); err != nil {
		return err
	}
	return h.trim(ctx, key)
}

// Messages 返回会话的全部条目（按 seq 升序）。
func (h *SQLite) Messages(ctx context.Context, key string) ([]Item, error) {
	msgs, err := h.st.Messages(ctx, key, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(msgs))
	for _, m := range msgs {
		it, err := toItem(m)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Reset 清空会话历史。
func (h *SQLite) Reset(ctx context.Context, key string) error {
	return h.st.ResetMessages(ctx, key)
}

// Trim 保留最近 n 条。
func (h *SQLite) Trim(ctx context.Context, key string, n int) error {
	_, err := h.st.TrimMessages(ctx, key, n)
	return err
}

// Search 实现 Searcher：按关键词检索本会话历史。
func (h *SQLite) Search(ctx context.Context, key, query string, limit int) ([]Hit, error) {
	found, err := h.st.SearchMessages(ctx, key, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(found))
	for _, f := range found {
		it, err := toItem(f.Message)
		if err != nil {
			return nil, err
		}
		hit := Hit{Item: it, Snippet: f.Snippet}
		if f.Before != nil {
			b, err := toItem(*f.Before)
			if err != nil {
				return nil, err
			}
			hit.Before = &b
		}
		if f.After != nil {
			a, err := toItem(*f.After)
			if err != nil {
				return nil, err
			}
			hit.After = &a
		}
		out = append(out, hit)
	}
	return out, nil
}

// trim 在超出上限时按策略裁剪。
func (h *SQLite) trim(ctx context.Context, key string) error {
	if h.max <= 0 {
		return nil
	}
	n, err := h.st.MessageCount(ctx, key)
	if err != nil {
		return err
	}
	if n <= h.max {
		return nil
	}
	items, err := h.Messages(ctx, key)
	if err != nil {
		return err
	}
	kept := items
	if h.trimmer != nil {
		kept = h.trimmer.Apply(items)
	}
	if len(kept) >= len(items) {
		return nil
	}
	_, err = h.st.TrimMessages(ctx, key, len(kept))
	return err
}

// roleOf 把历史条目类型映射成消息角色（供提示词拼装使用）。
func roleOf(k Kind) string {
	// KindUser 与 KindMarker 共用 default：marker 是内部条目，
	// 拼装提示词时会被 conversation 层跳过。
	//nolint:exhaustive // 见上：其余 Kind 共用 user 角色
	switch k {
	case KindAssistant, KindToolCall:
		return "assistant"
	case KindToolResult:
		return "tool"
	default:
		return "user"
	}
}

func atMillis(at time.Time) int64 {
	if at.IsZero() {
		return time.Now().UnixMilli()
	}
	return at.UnixMilli()
}

func toItem(m store.Message) (Item, error) {
	it := Item{
		Kind:       Kind(m.Kind),
		Content:    m.Content,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		At:         time.UnixMilli(m.CreatedAt),
	}
	if strings.TrimSpace(m.ToolCalls) != "" {
		var calls []ToolCall
		if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err != nil {
			return Item{}, fmt.Errorf("decode tool calls: %w", err)
		}
		it.ToolCalls = calls
	}
	return it, nil
}

// 导入复用 File 实现的 JSONL 行格式（fileRecord 定义在 file.go）。
// ErrImportSourceMissing 表示导入源文件不存在。
var ErrImportSourceMissing = errors.New("import source does not exist")

// ImportJSONL 把 JSONL 历史文件导入持久层（F-83 的迁移路径）。
//
// **幂等**：每条按其在该会话内的位置分配 seq，重复导入时 (session_key, seq) 冲突即跳过。
// 之所以不用"内容相同就跳过"，是因为对话里重复说同一句话完全正常——
// 那种判据会把合法的重复内容吃掉，而按位置分配序号不会。
func (h *SQLite) ImportJSONL(ctx context.Context, path string) (imported, skipped int, err error) {
	f, err := os.Open(path) //nolint:gosec // 路径来自运维显式配置
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("%w: %s", ErrImportSourceMissing, path)
		}
		return 0, 0, fmt.Errorf("open import source: %w", err)
	}
	defer func() { _ = f.Close() }()

	seqOf := map[string]int64{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec fileRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return imported, skipped, fmt.Errorf("parse line %d of %s: %w", lineNo, path, err)
		}
		if strings.TrimSpace(rec.Key) == "" {
			return imported, skipped, fmt.Errorf("line %d of %s has no session key", lineNo, path)
		}
		seqOf[rec.Key]++
		seq := seqOf[rec.Key]

		item := rec.Item
		if item.At.IsZero() {
			item.At = time.Now()
		}
		_, inserted, err := h.st.AppendMessage(ctx, store.Message{
			SessionKey: rec.Key,
			Seq:        seq,
			Role:       roleOf(item.Kind),
			Kind:       string(item.Kind),
			Content:    item.Content,
			Name:       item.Name,
			ToolCallID: item.ToolCallID,
			ToolCalls:  encodeToolCalls(item.ToolCalls),
			CreatedAt:  item.At.UnixMilli(),
		})
		if err != nil {
			return imported, skipped, fmt.Errorf("import line %d: %w", lineNo, err)
		}
		if inserted {
			imported++
		} else {
			skipped++
		}
	}
	if err := scanner.Err(); err != nil {
		return imported, skipped, fmt.Errorf("read import source: %w", err)
	}
	return imported, skipped, nil
}

func encodeToolCalls(calls []ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	raw, err := json.Marshal(calls)
	if err != nil {
		return ""
	}
	return string(raw)
}

var (
	_ History  = (*SQLite)(nil)
	_ Searcher = (*SQLite)(nil)
)
