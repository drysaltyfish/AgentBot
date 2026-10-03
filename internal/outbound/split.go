package outbound

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
)

// DefaultMaxSegments 是单次回复最多拆成几条消息。
const DefaultMaxSegments = 4

// blankLineRe 匹配"一个或多个空行"。
//
// \s 含换行，所以这个模式会把连续的多个空行一次性吃掉，不会切出空片段。
var blankLineRe = regexp.MustCompile(`\n\s*\n`)

// SplitParagraphs 按空行把一段文本切成多条消息。
//
// 聊天窗口里一大块文字读起来很累——真人是一条一条发的。这里只在空行处拆分：
// 段内的单个换行不拆，否则会把一句话切碎。
//
// maxSegments <= 0 时使用 DefaultMaxSegments；超出上限的尾部会合并进最后一条，
// 避免一次刷屏。
func SplitParagraphs(text string, maxSegments int) []string {
	if maxSegments <= 0 {
		maxSegments = DefaultMaxSegments
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	parts := blankLineRe.Split(normalized, -1)

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) > maxSegments {
		head := make([]string, 0, maxSegments)
		head = append(head, out[:maxSegments-1]...)
		head = append(head, strings.Join(out[maxSegments-1:], "\n\n"))
		return head
	}
	return out
}

// SendMany 依次发送多条消息，并在两条之间保持 delay 的间隔。
//
// 间隔让连发更像真人，也能避开平台的发送频率限制；等待是 ctx 感知的（不用 time.Sleep）。
// 某一条失败即返回已发送的条数与错误；已发出的消息无法撤回。
func (s *Sender) SendMany(ctx context.Context, target Target, texts []string, delay time.Duration) (int, error) {
	sent := 0
	for i, text := range texts {
		if i > 0 && delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return sent, ctx.Err()
			}
		}
		if _, err := s.Send(ctx, target, event.Message{event.Text(text)}); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}
