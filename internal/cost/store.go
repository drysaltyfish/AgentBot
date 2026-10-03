package cost

// Store 是成本快照的持久化接缝，由调用方注入实现（如 SQLite），本包不依赖
// 任何具体存储。实现必须并发安全。
type Store interface {
	// Load 读取上次保存的快照；无数据时应返回 (nil, nil) 或空快照。
	Load() (*Snapshot, error)
	// Save 覆盖保存完整快照；失败返回 error。
	Save(*Snapshot) error
}

// Snapshot 是成本聚合的可持久化快照。
type Snapshot struct {
	Buckets []Bucket
}

// Bucket 是某个 (scope, key, period, start) 维度的聚合。
//
// start 对 day 是 "YYYY-MM-DD"，对 month 是 "YYYY-MM"，对 total 为空串。
type Bucket struct {
	Scope  Scope
	Key    string
	Period Period
	Start  string
	Aggregate
}

// bucketKey 是内存聚合表的键。
type bucketKey struct {
	scope  Scope
	key    string
	period Period
	start  string
}
