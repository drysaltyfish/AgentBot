package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// PromptSnapshot 是一次 LLM 调用实际发送的消息序列的指纹快照（F-89）。
type PromptSnapshot struct {
	ID int64
	// SessionKey 是所属会话。
	SessionKey string
	// Seq 是会话内自增序号。
	Seq int64
	// Digest 是逐条消息的指纹（逗号分隔）。
	Digest string
	// MessageCount 是本轮发送的消息条数。
	MessageCount int64
	// MemoryDigest 是本轮记忆块的指纹（无记忆时为空）。
	MemoryDigest string
	// Relation 是与上一条快照的关系：
	// identical / extended / slid / memory_changed / diverged。
	Relation string
	// CommonPrefix 是与上一条从头相同的条数。
	CommonPrefix int64
	// SlidBy 仅在 slid 时有意义。
	SlidBy    int64
	CreatedAt int64
}

// RecordPromptSnapshot 记录一次快照并与同会话的上一条比较（F-89）。
//
// 比较结果**落库**：只记录指纹而不记录"和上一条比怎么样了"，事后就没法回答
// "前缀是从哪一轮开始不稳的"。
func (s *Store) RecordPromptSnapshot(ctx context.Context, sessionKey string, digest []string, memoryDigest string) (PromptSnapshot, error) {
	if strings.TrimSpace(sessionKey) == "" {
		return PromptSnapshot{}, fmt.Errorf("record prompt snapshot: session key must not be empty")
	}
	encoded := strings.Join(digest, ",")
	snap := PromptSnapshot{
		SessionKey: sessionKey, Digest: encoded,
		MessageCount: int64(len(digest)), MemoryDigest: memoryDigest,
	}

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		snap = PromptSnapshot{
			SessionKey: sessionKey, Digest: encoded,
			MessageCount: int64(len(digest)), MemoryDigest: memoryDigest,
		}

		var (
			prevSeq       int64
			prevDigest    string
			prevMemDigest string
		)
		err := tx.QueryRowContext(ctx,
			`SELECT seq, digest, memory_digest FROM prompt_snapshots WHERE session_key = ?
			 ORDER BY seq DESC LIMIT 1`, sessionKey).Scan(&prevSeq, &prevDigest, &prevMemDigest)
		switch {
		case err == nil:
			// 比较放在存储层之外会更干净，但那样要么多一次查询、要么把比较结果
			// 又传回来——放这里可以在同一事务里保证"取上一条 + 写这一条"是原子的。
			rel := compareDigest(prevDigest, encoded, prevMemDigest, memoryDigest)
			snap.Relation = rel.relation
			snap.CommonPrefix = int64(rel.commonPrefix)
			snap.SlidBy = int64(rel.slidBy)
			snap.Seq = prevSeq + 1
		case errorsIsNoRows(err):
			snap.Relation = "first"
			snap.Seq = 1
		default:
			return fmt.Errorf("read previous prompt snapshot: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO prompt_snapshots
			 (session_key, seq, digest, memory_digest, message_count, relation, common_prefix, slid_by, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snap.SessionKey, snap.Seq, snap.Digest, snap.MemoryDigest, snap.MessageCount,
			snap.Relation, snap.CommonPrefix, snap.SlidBy, nowMillis())
		if err != nil {
			return fmt.Errorf("insert prompt snapshot: %w", err)
		}
		snap.ID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("prompt snapshot id: %w", err)
		}
		return nil
	})
	return snap, err
}

type digestRelation struct {
	relation     string
	commonPrefix int
	slidBy       int
}

// compareDigest 判定两次快照的关系。它与 llm.ComparePrefix 是**同一套语义的两份实现**，
// 但作用在编码后的字符串上——存储层不该依赖表示层（llm 在本层之上）。
//
// 两份实现的关系词表必须逐字一致：本函数产出的字符串会落进
// `prompt_snapshots.relation`，而 reply 拿它去比对 llm 的常量（llm.RelationDiverged）。
// 改这边四个字面量中的任何一个，都会让那边的 case 分支静默失配，
// 表现为"前缀分叉告警不再出现"。这条耦合由
// internal/reply 的 Test_PrefixRelationVocabularyIsSharedAcrossPackages 守住。
//
// MemoryBlockIndex 是记忆块在消息序列里的位置（ADR-0002：system 之后、历史之前）。
//
// 记忆变化会让它**之后**的全部内容失效，因此"分歧点不超过这个位置"
// 正是"记忆变更导致的分歧"的特征。
const MemoryBlockIndex = 1

func compareDigest(prevEncoded, nextEncoded, prevMem, nextMem string) digestRelation {
	prev := splitDigest(prevEncoded)
	next := splitDigest(nextEncoded)
	cp := commonPrefix(prev, next)

	switch {
	case len(prev) == len(next) && cp == len(prev):
		return digestRelation{relation: "identical", commonPrefix: cp}
	case cp == len(prev):
		return digestRelation{relation: "extended", commonPrefix: cp}
	}
	for k := 1; k < len(prev); k++ {
		if prefixOf(prev[k:], next) {
			return digestRelation{relation: "slid", commonPrefix: cp, slidBy: k}
		}
	}
	// 记忆块变了，且分歧点就在记忆块处或之前 —— 这是**预期**变化（ADR-0002）。
	//
	// 真机实测：写入一条记忆后的下一轮必定走到这里，common_prefix 恰为 1
	// （只剩 system 相同）。若把它报成"意外分歧"，告警就永远在响，等于没有告警。
	if prevMem != nextMem && cp <= MemoryBlockIndex {
		return digestRelation{relation: RelationMemoryChanged, commonPrefix: cp}
	}
	return digestRelation{relation: "diverged", commonPrefix: cp}
}

func splitDigest(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func commonPrefix(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func prefixOf(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	return commonPrefix(prefix, full) == len(prefix)
}

// RelationMemoryChanged 表示前缀因记忆块变化而改变——**预期**行为（ADR-0002）。
const RelationMemoryChanged = "memory_changed"

// ListPromptSnapshots 返回会话最近的快照（按 seq 升序）。
func (s *Store) ListPromptSnapshots(ctx context.Context, sessionKey string, limit int) ([]PromptSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_key, seq, digest, memory_digest, message_count, relation, common_prefix, slid_by, created_at
		 FROM prompt_snapshots WHERE session_key = ? ORDER BY seq DESC LIMIT ?`, sessionKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list prompt snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]PromptSnapshot, 0, limit)
	for rows.Next() {
		var p PromptSnapshot
		if err := rows.Scan(&p.ID, &p.SessionKey, &p.Seq, &p.Digest, &p.MemoryDigest, &p.MessageCount,
			&p.Relation, &p.CommonPrefix, &p.SlidBy, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan prompt snapshot: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prompt snapshots: %w", err)
	}
	// 翻回时间序。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// CountDivergedSnapshots 返回"意料之外的前缀变化"次数（观测用）。
func (s *Store) CountDivergedSnapshots(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM prompt_snapshots WHERE relation = 'diverged'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count diverged snapshots: %w", err)
	}
	return n, nil
}

// PrunePromptSnapshots 每个会话只保留最近 keep 条，保证表有界（F-89 的环形保留）。
func (s *Store) PrunePromptSnapshots(ctx context.Context, keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	var n int64
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM prompt_snapshots WHERE id IN (
				SELECT id FROM prompt_snapshots p
				WHERE (SELECT count(*) FROM prompt_snapshots q
					WHERE q.session_key = p.session_key AND q.seq > p.seq) >= ?
			)`, keep)
		if err != nil {
			return fmt.Errorf("prune prompt snapshots: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

func errorsIsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
