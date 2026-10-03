package reply

import (
	"context"
	"sync"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/transport"
)

// quotedResolver 解析引用消息的内容（QQ 的"回复"功能）。
//
// 为什么需要它：OneBot 的 reply 段只给一个 message_id，**被引用的内容不在事件里**。
// 不解析的话，模型只看到 "[回复]"，根本不知道对方在回哪句话——
// 表现为"机器人看不懂我在回复它过去的话"。
//
// 缓存：同一条消息常被反复引用，缓存能省掉绝大部分 get_msg；
// TTL：消息可以被撤回，缓存过期后重新解析，避免长期显示已撤回的内容。
type quotedResolver struct {
	mu    sync.Mutex
	cache map[string]quotedEntry
}

type quotedEntry struct {
	text string
	at   time.Time
}

// quotedCacheTTL 是引用解析结果的缓存时长。
const quotedCacheTTL = 10 * time.Minute

func newQuotedResolver() *quotedResolver {
	return &quotedResolver{cache: map[string]quotedEntry{}}
}

// resolve 把消息里所有引用段的内容填好，返回成功填充的段数。
//
// 失败**不阻断**消息处理：解析不到就保留 "[回复]" 占位符并告警，
// 宁可信息少一点，也不能因为一次 API 调用失败就丢掉整条消息。
func (r *quotedResolver) resolve(ctx context.Context, caller transport.Caller, msg event.Message) int {
	ids := msg.ReplyIDs()
	if len(ids) == 0 {
		return 0
	}
	filled := 0
	for _, id := range ids {
		text, ok := r.lookup(id)
		if !ok {
			var err error
			text, err = transport.GetMsgText(ctx, caller, id)
			if err != nil {
				// 调用方负责记日志；这里只保证不阻断。
				continue
			}
			if text == "" {
				continue
			}
			r.store(id, text)
		}
		filled += msg.SetReplyText(id, text)
	}
	return filled
}

func (r *quotedResolver) lookup(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[id]
	if !ok || time.Since(e.at) > quotedCacheTTL {
		return "", false
	}
	return e.text, true
}

func (r *quotedResolver) store(id, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 有界：引用解析是热路径，缓存不能无界增长。
	const maxEntries = 512
	if len(r.cache) >= maxEntries {
		for k, e := range r.cache {
			if time.Since(e.at) > quotedCacheTTL {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxEntries {
			for k := range r.cache {
				delete(r.cache, k)
				break
			}
		}
	}
	r.cache[id] = quotedEntry{text: text, at: time.Now()}
}
