package moderation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// 黑名单相关哨兵错误。
var (
	// ErrProtected 表示目标在保护名单内（机器人自身/超管），拒绝封禁。
	ErrProtected = errors.New("moderation: 受保护主体不可封禁")
	// ErrUnknownBanKind 表示无法识别的黑名单维度。
	ErrUnknownBanKind = errors.New("moderation: 未知黑名单维度")
	// ErrNilBanStore 表示未提供黑名单存储。
	ErrNilBanStore = errors.New("moderation: nil 黑名单存储")
)

// BanKind 是黑名单维度。
type BanKind uint8

// 黑名单维度；零值代表用户。
const (
	// BanUser 按用户 ID 封禁。
	BanUser BanKind = iota
	// BanGroup 按群 ID 封禁。
	BanGroup
	// BanIP 按来源 IP 封禁。
	BanIP
)

// String 返回稳定的字符串形式，同时作为文件存储中的类型标记。
func (k BanKind) String() string {
	switch k {
	case BanUser:
		return "user"
	case BanGroup:
		return "group"
	case BanIP:
		return "ip"
	default:
		return "unknown"
	}
}

// valid 报告维度是否为已定义的取值。
func (k BanKind) valid() bool {
	switch k {
	case BanUser, BanGroup, BanIP:
		return true
	default:
		return false
	}
}

// ParseBanKind 解析字符串形式的维度；无法识别时返回 false。
func ParseBanKind(s string) (BanKind, bool) {
	switch s {
	case "user", "u":
		return BanUser, true
	case "group", "g":
		return BanGroup, true
	case "ip":
		return BanIP, true
	default:
		return BanUser, false
	}
}

// BanEntry 是一条黑名单记录。
type BanEntry struct {
	// Kind 是维度。
	Kind BanKind
	// ID 是维度内的标识（用户/群 ID 的十进制字符串，或 IP）。
	ID string
	// Reason 是封禁原因。
	Reason string
	// CreatedAt 是创建时间。
	CreatedAt time.Time
	// ExpiresAt 是过期时间；零值表示永久。
	ExpiresAt time.Time
}

// Expired 报告条目在 now 是否已经过期。
func (e BanEntry) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// banKey 把维度与 ID 编码成快照键；ID 不含 "|"，故该分隔无歧义。
func banKey(kind BanKind, id string) string {
	return kind.String() + "|" + id
}

// BanStore 是黑名单的持久化抽象；实现必须并发安全。
type BanStore interface {
	// Add 新增或覆盖一条记录。
	Add(BanEntry) error
	// Remove 删除记录，返回是否存在。
	Remove(BanKind, string) (bool, error)
	// Get 读取记录。
	Get(BanKind, string) (BanEntry, bool, error)
	// List 返回全部记录的副本，顺序无关。
	List() ([]BanEntry, error)
}

// MemoryBanStore 是基于 map 的内存实现，用于测试与临时运行。
type MemoryBanStore struct {
	mu sync.RWMutex
	m  map[string]BanEntry
}

// NewMemoryBanStore 构造空的 MemoryBanStore。
func NewMemoryBanStore() *MemoryBanStore {
	return &MemoryBanStore{m: make(map[string]BanEntry)}
}

// Add 实现 BanStore。
func (s *MemoryBanStore) Add(e BanEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string]BanEntry)
	}
	s.m[banKey(e.Kind, e.ID)] = e
	return nil
}

// Remove 实现 BanStore。
func (s *MemoryBanStore) Remove(k BanKind, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := banKey(k, id)
	if _, ok := s.m[key]; !ok {
		return false, nil
	}
	delete(s.m, key)
	return true, nil
}

// Get 实现 BanStore。
func (s *MemoryBanStore) Get(k BanKind, id string) (BanEntry, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[banKey(k, id)]
	return e, ok, nil
}

// List 实现 BanStore，返回快照副本。
func (s *MemoryBanStore) List() ([]BanEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BanEntry, 0, len(s.m))
	for _, e := range s.m {
		out = append(out, e)
	}
	return out, nil
}

// banWire 是 FileBanStore 的 JSON 表示单元。
type banWire struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// FileBanStore 把黑名单持久化为 JSON 文件，采用“临时文件 + rename”原子替换；
// 打开时载入已有内容，因此重启后封禁仍然生效。
type FileBanStore struct {
	path string

	mu sync.RWMutex
	m  map[string]BanEntry
}

// NewFileBanStore 打开（或创建）指定路径的文件存储。文件不存在时返回空存储；
// 内容非法时返回 error；path 不能为空。
func NewFileBanStore(path string) (*FileBanStore, error) {
	if path == "" {
		return nil, fmt.Errorf("moderation: 文件黑名单路径为空")
	}
	s := &FileBanStore{path: path, m: make(map[string]BanEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("moderation: 读取黑名单 %q: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}
	var entries []banWire
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("moderation: 解析黑名单 %q: %w", path, err)
	}
	for _, w := range entries {
		kind, ok := ParseBanKind(w.Kind)
		if !ok {
			return nil, fmt.Errorf("moderation: 黑名单 %q 含未知维度 %q: %w", path, w.Kind, ErrUnknownBanKind)
		}
		e := BanEntry{Kind: kind, ID: w.ID, Reason: w.Reason, CreatedAt: w.CreatedAt, ExpiresAt: w.ExpiresAt}
		s.m[banKey(kind, w.ID)] = e
	}
	return s, nil
}

// Add 实现 BanStore；落盘失败时回滚内存状态。
func (s *FileBanStore) Add(e BanEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := banKey(e.Kind, e.ID)
	prev, had := s.m[key]
	s.m[key] = e
	if err := s.flushLocked(); err != nil {
		if had {
			s.m[key] = prev
		} else {
			delete(s.m, key)
		}
		return err
	}
	return nil
}

// Remove 实现 BanStore；落盘失败时回滚内存状态。
func (s *FileBanStore) Remove(k BanKind, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := banKey(k, id)
	prev, had := s.m[key]
	if !had {
		return false, nil
	}
	delete(s.m, key)
	if err := s.flushLocked(); err != nil {
		s.m[key] = prev
		return false, err
	}
	return true, nil
}

// Get 实现 BanStore。
func (s *FileBanStore) Get(k BanKind, id string) (BanEntry, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.m[banKey(k, id)]
	return e, ok, nil
}

// List 实现 BanStore，返回快照副本。
func (s *FileBanStore) List() ([]BanEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BanEntry, 0, len(s.m))
	for _, e := range s.m {
		out = append(out, e)
	}
	return out, nil
}

// flushLocked 在持锁状态下原子写回整个文件。
func (s *FileBanStore) flushLocked() error {
	entries := make([]banWire, 0, len(s.m))
	for _, e := range s.m {
		entries = append(entries, banWire{
			Kind:      e.Kind.String(),
			ID:        e.ID,
			Reason:    e.Reason,
			CreatedAt: e.CreatedAt,
			ExpiresAt: e.ExpiresAt,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].ID < entries[j].ID
	})
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("moderation: 编码黑名单: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("moderation: 创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	// rename 成功后该 defer 无害地失败；失败路径下负责清理临时文件。
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("moderation: 写临时文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("moderation: 同步临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("moderation: 关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("moderation: 原子替换 %q: %w", s.path, err)
	}
	return nil
}

// BlacklistOptions 是 Blacklist 的构造参数。
type BlacklistOptions struct {
	// SelfID 是机器人自身 ID，永不可被封禁。
	SelfID int64
	// SuperUsers 是超管用户 ID 列表，永不可被封禁（白名单优先）。
	SuperUsers []int64
	// Now 提供时间源，nil 表示 time.Now。
	Now func() time.Time
	// Warn 接收非致命告警（如清理过期封禁失败），可为 nil。
	Warn func(string)
}

// Blacklist 在 BanStore 之上维护一份只读快照，使查询保持 O(1) 且并发安全。
//
// 读多写少：查询只持读锁遍历 map；写入或惰性清理过期项时才取写锁。
type Blacklist struct {
	store     BanStore
	now       func() time.Time
	warn      func(string)
	protected map[string]struct{}

	mu   sync.RWMutex
	snap map[string]BanEntry
}

// NewBlacklist 从 store 载入快照；store 为 nil 或读取失败时返回 error。
func NewBlacklist(store BanStore, opts BlacklistOptions) (*Blacklist, error) {
	if store == nil {
		return nil, ErrNilBanStore
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	b := &Blacklist{
		store:     store,
		now:       now,
		warn:      opts.Warn,
		protected: make(map[string]struct{}, len(opts.SuperUsers)+1),
		snap:      make(map[string]BanEntry),
	}
	for _, id := range opts.SuperUsers {
		b.protected[banKey(BanUser, idString(id))] = struct{}{}
	}
	if opts.SelfID != 0 {
		b.protected[banKey(BanUser, idString(opts.SelfID))] = struct{}{}
	}
	if err := b.Reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// Reload 从 store 重新读取快照；用于外部修改存储后的同步。
func (b *Blacklist) Reload() error {
	entries, err := b.store.List()
	if err != nil {
		return fmt.Errorf("moderation: 载入黑名单: %w", err)
	}
	snap := make(map[string]BanEntry, len(entries))
	for _, e := range entries {
		snap[banKey(e.Kind, e.ID)] = e
	}
	b.mu.Lock()
	b.snap = snap
	b.mu.Unlock()
	return nil
}

// Store 返回底层存储（便于复用/测试）。
func (b *Blacklist) Store() BanStore {
	if b == nil {
		return nil
	}
	return b.store
}

// Protected 报告主体是否在保护名单内。
func (b *Blacklist) Protected(kind BanKind, id string) bool {
	if b == nil {
		return false
	}
	_, ok := b.protected[banKey(kind, id)]
	return ok
}

// Ban 新增一条封禁；ttl <= 0 表示永久。保护名单内的主体返回 ErrProtected。
func (b *Blacklist) Ban(kind BanKind, id, reason string, ttl time.Duration) error {
	if b == nil {
		return ErrNilBanStore
	}
	now := b.now()
	entry := BanEntry{Kind: kind, ID: id, Reason: reason, CreatedAt: now}
	if ttl > 0 {
		entry.ExpiresAt = now.Add(ttl)
	}
	return b.Add(entry)
}

// Add 新增或覆盖一条封禁记录，并同步更新快照。
func (b *Blacklist) Add(entry BanEntry) error {
	if b == nil {
		return ErrNilBanStore
	}
	if entry.ID == "" {
		return fmt.Errorf("moderation: 空黑名单 ID")
	}
	if !entry.Kind.valid() {
		return fmt.Errorf("%w: %d", ErrUnknownBanKind, entry.Kind)
	}
	if b.Protected(entry.Kind, entry.ID) {
		return fmt.Errorf("%w: %s %s", ErrProtected, entry.Kind, entry.ID)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = b.now()
	}
	if err := b.store.Add(entry); err != nil {
		return fmt.Errorf("moderation: 写入黑名单: %w", err)
	}
	b.mu.Lock()
	b.snap[banKey(entry.Kind, entry.ID)] = entry
	b.mu.Unlock()
	return nil
}

// Unban 解除封禁，返回该主体此前是否存在。
func (b *Blacklist) Unban(kind BanKind, id string) (bool, error) {
	if b == nil {
		return false, ErrNilBanStore
	}
	existed, err := b.store.Remove(kind, id)
	if err != nil {
		return false, fmt.Errorf("moderation: 移除黑名单: %w", err)
	}
	b.mu.Lock()
	delete(b.snap, banKey(kind, id))
	b.mu.Unlock()
	return existed, nil
}

// Banned 报告主体是否处于封禁中。查询 O(1)，过期项惰性清理；
// 保护名单内的主体始终视为未封禁。
func (b *Blacklist) Banned(kind BanKind, id string) (BanEntry, bool) {
	if b == nil || b.Protected(kind, id) {
		return BanEntry{}, false
	}
	key := banKey(kind, id)
	b.mu.RLock()
	entry, ok := b.snap[key]
	b.mu.RUnlock()
	if !ok {
		return BanEntry{}, false
	}
	if entry.Expired(b.now()) {
		b.mu.Lock()
		delete(b.snap, key)
		b.mu.Unlock()
		if _, err := b.store.Remove(kind, id); err != nil {
			b.warnf("moderation: 清理过期封禁失败: %v", err)
		}
		return BanEntry{}, false
	}
	return entry, true
}

// List 返回未过期条目的快照，按维度、ID 排序。
func (b *Blacklist) List() []BanEntry {
	if b == nil {
		return nil
	}
	now := b.now()
	b.mu.RLock()
	out := make([]BanEntry, 0, len(b.snap))
	for _, e := range b.snap {
		if e.Expired(now) {
			continue
		}
		out = append(out, e)
	}
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// warnf 在注入告警回调时记录一条告警。
func (b *Blacklist) warnf(format string, args ...any) {
	if b.warn == nil {
		return
	}
	b.warn(fmt.Sprintf(format, args...))
}

// parseID 把十进制 ID 字符串解析为 int64，供管理命令校验参数。
func parseID(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("moderation: 非法 ID %q: %w", s, err)
	}
	return v, nil
}

// 编译期断言：两种存储实现必须始终满足接口。
var (
	_ BanStore = (*MemoryBanStore)(nil)
	_ BanStore = (*FileBanStore)(nil)
)
