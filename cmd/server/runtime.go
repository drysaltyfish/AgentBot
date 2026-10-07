package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/outbound"
	"github.com/drysaltyfish/agentbot/internal/router"
	"github.com/drysaltyfish/agentbot/internal/session"
	"github.com/drysaltyfish/agentbot/internal/store"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// 编译期断言：装配层的适配器必须始终满足它们所实现的接口（F-79）。
var (
	_ bot.Component        = namedComponent{}
	_ session.PendingStore = pendingStoreAdapter{}
)

// buildTransportAuth 按配置构造传输鉴权；这是**唯一**的构造点。
//
// serve 与 --selftest 必须用同一份。曾经 --selftest 自己拼了一个：少了签名密钥、
// 也没跑 Validate()。于是配了签名校验的部署里，"服务连得上、自检连不上"，
// 而自检恰恰是排障时最该可信的那条路径。
//
// 这类"两处各拼一次"的分歧只在特定配置下暴露，平常看起来完全正常。
func buildTransportAuth(cfg *config.Config) (*transport.Auth, error) {
	auth := transport.NewAuth(
		stringOr(cfg.Transport.AccessToken, ""),
		stringOr(cfg.Transport.SignatureSecret, ""),
		cfg.Transport.IPAllowlist,
	)
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	return auth, nil
}

// useSingleflight 把单飞的**两半**一起注册：mid 的判定与 post 的释放。
//
// 它们是成对的，而且必须成对——只注册 `Rule()` 而漏掉 `Release()` 的后果
// 不是"单飞失效"，而是**永久失效**：Rule 会为 key 占位，而没有任何地方释放它，
// 于是该 key 之后**每一条**消息都会撞上 busy 被拒绝。
// 用户端表现是"机器人每个会话只答一次，之后永远沉默"（或一直回"正在处理中"），
// 而这里有唯一一处能出错的地方：漏了一行注册。
//
// 用一个函数绑住两半，比加一条"记得两处都要注册"的注释可靠。
func useSingleflight[K comparable](engine *router.Engine, sf *router.Singleflight[K]) {
	if engine == nil || sf == nil {
		return
	}
	engine.UseMid(sf.Rule())
	engine.UsePost(sf.Release())
}

// eventJob 是一次待分发的事件（F-20 背压队列的元素）。
//
// 队列元素必须自带 ctx 与 caller：它们决定这次分发属于哪条链路、用哪个通道回包。
type eventJob struct {
	ctx    context.Context
	event  *event.Event
	caller transport.Caller
}

// callerBox 让工具的 API 通道可以**迟到绑定**。
//
// 需要它是因为传输客户端在工具装配之后才创建；直接注入会迫使初始化顺序倒置，
// 而顺序一乱很容易再踩一次"在错误的地方调 API"（引用解析就踩过）。
type callerBox struct {
	mu sync.RWMutex
	c  transport.Caller
}

func (b *callerBox) Caller() transport.Caller {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.c
}

func (b *callerBox) set(c transport.Caller) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.c = c
}

// pendingStoreAdapter 把持久层适配成会话层的在途记录接口（F-86）。
//
// 中间隔一层是因为会话层不该知道 SQL 长什么样：它只需要"存一条等待 / 结束一条等待"。
type pendingStoreAdapter struct{ st *store.Store }

func (a pendingStoreAdapter) SavePending(ctx context.Context, r session.PendingRecord) error {
	return a.st.UpsertPending(ctx, store.Pending{
		ID: r.ID, SessionKey: r.SessionKey, Kind: r.Kind, Payload: r.Payload,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Status: store.PendingStatusPending,
	})
}

func (a pendingStoreAdapter) FinishPending(ctx context.Context, id, status, note string) error {
	return a.st.CompletePending(ctx, id, status, note)
}

// pendingSessionTarget 由会话键还原出投递目标。
//
// 群消息投群、私聊投人——**不猜测**：键里还原不出的信息不编造。
func pendingSessionTarget(key string) (outbound.Target, bool) {
	parts := strings.Split(key, ":")
	if len(parts) < 3 {
		return outbound.Target{}, false
	}
	groupID, err1 := strconv.ParseInt(parts[1], 10, 64)
	userID, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return outbound.Target{}, false
	}
	if groupID != 0 {
		return outbound.GroupTarget(groupID), true
	}
	if userID != 0 {
		return outbound.PrivateTarget(userID), true
	}
	return outbound.Target{}, false
}

// recoverPending 处理重启时残留的在途记录（F-86）。
//
// **诚实的说明**：我们只能通知，不能真正续跑。等待下一条消息与等待人工审批都挂在
// 一次正在执行的调用上（阻塞在 channel 上），进程重启后那次调用已经不存在，
// 没有东西可以"恢复"。因此这里做两件事：
//  1. 未过期的：告诉对方"刚才重启了，那个等待被打断"，避免一直干等；
//  2. 已过期的：告诉对方"等太久了，已作废"。
//
// 状态分别置为 orphaned / expired，**保留记录**而不是删除——
// "谁在什么时候批准/超时"正是审计要回答的。
//
// 之所以不做成"真正续跑"：那需要把等待变成可重放的持久工作流（谁在等、等到什么、
// 等到之后干什么），那是一个独立得多的特性，不该塞进这一条里假装完成。
func recoverPending(ctx context.Context, st *store.Store, sender *outbound.Sender, lg *observe.Logger) {
	plog := lg.Component("pending")

	rows, err := st.ListPending(ctx, "")
	if err != nil {
		plog.Warn("cannot list pending operations; recovery skipped", "error", err)
		return
	}
	expired, err := st.ExpirePending(ctx, 0)
	if err != nil {
		plog.Warn("cannot expire stale pending operations", "error", err)
	}

	notify := func(p store.Pending, expiredAlready bool, note string) {
		target, ok := pendingSessionTarget(p.SessionKey)
		if !ok {
			// 还原不出目标就不猜：标成孤儿并告警。
			if err := st.CompletePending(ctx, p.ID, store.PendingStatusOrphaned,
				"无法从会话键还原投递目标"); err != nil {
				plog.Warn("cannot mark pending as orphaned", "error", err, "id", p.ID)
			}
			plog.Warn("pending operation has an unparsable session key", "id", p.ID, "session_key", p.SessionKey)
			return
		}
		if _, serr := sender.SendMany(ctx, target, []string{note}, 0); serr != nil {
			plog.Warn("cannot notify the session about an interrupted wait", "error", serr, "id", p.ID)
			return
		}
		status := store.PendingStatusOrphaned
		if expiredAlready {
			status = store.PendingStatusExpired
		}
		if err := st.CompletePending(ctx, p.ID, status, "重启后已通知原会话"); err != nil {
			plog.Warn("cannot close pending operation", "error", err, "id", p.ID)
		}
	}

	for _, p := range expired {
		notify(p, true, "刚才那件事等太久了，已经作废啦；要办的话请再说一次～")
	}

	expiredIDs := make(map[string]struct{}, len(expired))
	for _, p := range expired {
		expiredIDs[p.ID] = struct{}{}
	}
	notified := 0
	for _, p := range rows {
		if _, done := expiredIDs[p.ID]; done {
			continue
		}
		notified++
		notify(p, false, "刚才我重启了一下，之前正在等的那件事被打断了；麻烦你再发起一次～")
	}
	plog.Info("pending operations recovered",
		"total", len(rows), "expired", len(expired), "notified", notified)

	// 有界性：清掉早已结束的记录。
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if pruned, perr := st.PrunePending(ctx, cutoff); perr != nil {
		plog.Warn("cannot prune pending history", "error", perr)
	} else if pruned > 0 {
		plog.Info("pruned finished pending records", "count", pruned)
	}
}

// namedComponent 把裸函数适配成 bot.Component。
type namedComponent struct {
	name  string
	close func(ctx context.Context) error
}

func (c namedComponent) Name() string { return c.name }

func (c namedComponent) Close(ctx context.Context) error { return c.close(ctx) }

// segmentDetail 渲染非文本段的全部字段，用于确认平台真实载荷。
//
// 例：image{file=a.jpg,file_size=12345,sub_type=1} face{id=4}
func segmentDetail(m event.Message) string {
	parts := make([]string, 0, len(m))
	for _, seg := range m {
		if seg.Type == event.TypeText {
			continue
		}
		keys := make([]string, 0, len(seg.Data))
		for k := range seg.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kv := make([]string, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, k+"="+seg.Data[k])
		}
		parts = append(parts, seg.Type+"{"+strings.Join(kv, ",")+"}")
	}
	return strings.Join(parts, " ")
}

// segmentTypes 把消息段类型拼成 "text+face+image" 形式，便于在日志里看清平台真实载荷。
func segmentTypes(m event.Message) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for _, seg := range m {
		parts = append(parts, seg.Type)
	}
	return strings.Join(parts, "+")
}

// legacyTraceID 是 trace 包不可用时的兜底标识（F-72 之前的形式）。
//
// 保留它是因为「生成随机 trace id」理论上可能失败（crypto/rand 出错）；
// 那种时候宁可日志里的 trace_id 不那么标准，也不能让一次事件处理挂掉。
func legacyTraceID(ev *event.Event) string {
	return fmt.Sprintf("%d-%d-%s", ev.SelfID, ev.Time.Unix(), ev.MessageID.String())
}
