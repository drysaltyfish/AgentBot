package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrSessionNotFound 表示台账里没有这个会话。
var ErrSessionNotFound = errors.New("session not found")

// UsageDelta 是一次模型调用产生的增量。
//
// 全部字段都是**增量**：累加必须由数据库做（UPDATE ... SET x = x + ?），
// 而不是"读出来加一加再写回"——后者在并发下会丢增量，而且丢得无声无息。
type UsageDelta struct {
	Requests        int64
	ToolCalls       int64
	InputTokens     int64
	OutputTokens    int64
	CacheHitTokens  int64
	CacheMissTokens int64
	ReasoningTokens int64
	// CostUSD 是按**当时**价格算出的估计成本。
	CostUSD        float64
	PricingVersion string
	// At 为 0 时用当前时间。
	At int64
}

// Usage 是累计用量快照。
type Usage struct {
	SessionKey       string
	FirstSeen        int64
	LastActive       int64
	Requests         int64
	ToolCalls        int64
	InputTokens      int64
	OutputTokens     int64
	CacheHitTokens   int64
	CacheMissTokens  int64
	ReasoningTokens  int64
	EstimatedCostUSD float64
	PricingVersion   string
	// MessageCount 由 messages 表实时统计，不是累加值——
	// 累加的计数会与实际行数漂移，而这类漂移没人会发现。
	MessageCount int64
}

// CacheHitRatio 返回缓存命中率（0~1）。
//
// 分母只用 hit + miss：DeepSeek 的 prompt_tokens 含命中部分，
// 直接拿 prompt_tokens 当分母会把比率算小。
func (u Usage) CacheHitRatio() float64 {
	total := u.CacheHitTokens + u.CacheMissTokens
	if total <= 0 {
		return 0
	}
	return float64(u.CacheHitTokens) / float64(total)
}

// AddUsage 原子累加一次调用的用量。
//
// 用 upsert + "列 = 列 + 增量" 的形式：整条语句在数据库内完成，天然并发安全。
func (s *Store) AddUsage(ctx context.Context, sessionKey string, d UsageDelta) error {
	if strings.TrimSpace(sessionKey) == "" {
		return fmt.Errorf("add usage: session key must not be empty")
	}
	at := d.At
	if at == 0 {
		at = nowMillis()
	}
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (
				session_key, first_seen, last_active, requests, tool_calls,
				input_tokens, output_tokens, cache_hit_tokens, cache_miss_tokens,
				reasoning_tokens, estimated_cost_usd, pricing_version
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_key) DO UPDATE SET
				last_active        = excluded.last_active,
				requests           = sessions.requests + excluded.requests,
				tool_calls         = sessions.tool_calls + excluded.tool_calls,
				input_tokens       = sessions.input_tokens + excluded.input_tokens,
				output_tokens      = sessions.output_tokens + excluded.output_tokens,
				cache_hit_tokens   = sessions.cache_hit_tokens + excluded.cache_hit_tokens,
				cache_miss_tokens  = sessions.cache_miss_tokens + excluded.cache_miss_tokens,
				reasoning_tokens   = sessions.reasoning_tokens + excluded.reasoning_tokens,
				estimated_cost_usd = sessions.estimated_cost_usd + excluded.estimated_cost_usd,
				pricing_version    = excluded.pricing_version
		`,
			sessionKey, at, at, d.Requests, d.ToolCalls,
			d.InputTokens, d.OutputTokens, d.CacheHitTokens, d.CacheMissTokens,
			d.ReasoningTokens, d.CostUSD, d.PricingVersion)
		if err != nil {
			return fmt.Errorf("accumulate usage: %w", err)
		}
		return nil
	})
}

const usageCols = `s.session_key, s.first_seen, s.last_active, s.requests, s.tool_calls,
	s.input_tokens, s.output_tokens, s.cache_hit_tokens, s.cache_miss_tokens,
	s.reasoning_tokens, s.estimated_cost_usd, s.pricing_version,
	COALESCE(m.n, 0)`

func scanUsage(sc interface{ Scan(...any) error }) (Usage, error) {
	var u Usage
	err := sc.Scan(&u.SessionKey, &u.FirstSeen, &u.LastActive, &u.Requests, &u.ToolCalls,
		&u.InputTokens, &u.OutputTokens, &u.CacheHitTokens, &u.CacheMissTokens,
		&u.ReasoningTokens, &u.EstimatedCostUSD, &u.PricingVersion, &u.MessageCount)
	return u, err
}

// SessionUsage 返回单个会话的累计用量。
func (s *Store) SessionUsage(ctx context.Context, sessionKey string) (Usage, error) {
	q := `SELECT ` + usageCols + `
		FROM sessions s
		LEFT JOIN (SELECT session_key, count(*) AS n FROM messages GROUP BY session_key) m
			ON m.session_key = s.session_key
		WHERE s.session_key = ?`
	u, err := scanUsage(s.db.QueryRowContext(ctx, q, sessionKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Usage{}, ErrSessionNotFound
		}
		return Usage{}, fmt.Errorf("query session usage: %w", err)
	}
	return u, nil
}

// UsageTotals 返回全库累计用量（跨会话汇总）。
func (s *Store) UsageTotals(ctx context.Context) (Usage, error) {
	// 全部聚合都要 COALESCE：空表上 MIN/MAX/SUM 返回 NULL，
	// 而 NULL 扫进 int64 会直接报错——新装环境第一次查台账就会撞上。
	q := `SELECT '',
			COALESCE(MIN(s.first_seen), 0), COALESCE(MAX(s.last_active), 0),
			COALESCE(SUM(s.requests), 0), COALESCE(SUM(s.tool_calls), 0),
			COALESCE(SUM(s.input_tokens), 0), COALESCE(SUM(s.output_tokens), 0),
			COALESCE(SUM(s.cache_hit_tokens), 0), COALESCE(SUM(s.cache_miss_tokens), 0),
			COALESCE(SUM(s.reasoning_tokens), 0), COALESCE(SUM(s.estimated_cost_usd), 0), '',
			(SELECT count(*) FROM messages)
		FROM sessions s`
	u, err := scanUsage(s.db.QueryRowContext(ctx, q))
	if err != nil {
		return Usage{}, fmt.Errorf("query usage totals: %w", err)
	}
	return u, nil
}

// TopSessions 按估计成本降序返回前 limit 个会话。
func (s *Store) TopSessions(ctx context.Context, limit int) ([]Usage, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `SELECT ` + usageCols + `
		FROM sessions s
		LEFT JOIN (SELECT session_key, count(*) AS n FROM messages GROUP BY session_key) m
			ON m.session_key = s.session_key
		ORDER BY s.estimated_cost_usd DESC, s.last_active DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query top sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Usage, 0, limit)
	for rows.Next() {
		u, err := scanUsage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session usage: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return out, nil
}

// ResetUsage 清空台账（导出/维护用）。
func (s *Store) ResetUsage(ctx context.Context) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions`)
		if err != nil {
			return fmt.Errorf("reset usage: %w", err)
		}
		return nil
	})
}
