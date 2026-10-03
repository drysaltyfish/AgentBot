package router

import "sync"

// Singleflight 保证同一 key 在同一时刻只有一个处理在执行（F-17）。
//
// 典型场景：用户连点两次，第二次应该在入口就被拒掉，而不是触发第二次昂贵调用。
// 占位表只活在进程内——重启即清空是期望行为，不是缺陷。
type Singleflight[K comparable] struct {
	mu       sync.Mutex
	inflight map[K]struct{}
	key      func(*Ctx) K
	onReject func(*Ctx)
}

// NewSingleflight 构造中间件；key 由调用方给出（常用 UserID 或 UserID+GroupID）。
func NewSingleflight[K comparable](key func(*Ctx) K) *Singleflight[K] {
	return &Singleflight[K]{inflight: map[K]struct{}{}, key: key}
}

// OnReject 设置被拒绝时的回调（例如回一句"正在处理中"）。
func (s *Singleflight[K]) OnReject(fn func(*Ctx)) *Singleflight[K] {
	s.onReject = fn
	return s
}

// Rule 挂到 mid：key 已在处理 -> 拒绝；否则占位并放行。
func (s *Singleflight[K]) Rule() Rule {
	return func(c *Ctx) bool {
		if c == nil || c.Event == nil {
			return true
		}
		k := s.key(c)
		s.mu.Lock()
		_, busy := s.inflight[k]
		if !busy {
			s.inflight[k] = struct{}{}
		}
		s.mu.Unlock()

		if busy {
			if s.onReject != nil {
				s.onReject(c)
			}
			return false
		}
		return true
	}
}

// Release 返回 **post** 钩子：释放占位。
//
// 必须挂在 post 而不是 mid 的清理逻辑里：引擎在 Handler panic 后仍然执行 post，
// 因此占用一定被释放；依赖 finalizer 或超时释放都会留下永久卡住的 key。
func (s *Singleflight[K]) Release() Handler {
	return func(c *Ctx) {
		if c == nil || c.Event == nil {
			return
		}
		k := s.key(c)
		s.mu.Lock()
		delete(s.inflight, k) // key 不存在时 delete 是安全的，不需要类型断言
		s.mu.Unlock()
	}
}

// Inflight 返回当前占位数量（观测与测试用）。
func (s *Singleflight[K]) Inflight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inflight)
}
