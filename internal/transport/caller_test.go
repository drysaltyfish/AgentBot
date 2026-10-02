package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/event"
	"github.com/drysaltyfish/agentbot/internal/retry"
)

type stubCaller struct {
	calls atomic.Int64
	last  Request
	resp  Response
	err   error
	failN int64
}

func (s *stubCaller) Call(ctx context.Context, req Request) (Response, error) {
	n := s.calls.Add(1)
	s.last = req
	if s.failN > 0 && n <= s.failN {
		return Response{}, errors.New("transient")
	}
	return s.resp, s.err
}

func Test_F05_ChainPenetratesAndReturnsOriginalResponse(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{RetCode: 0, Data: json.RawMessage("{\"message_id\":7}")}}

	var order []string
	mw := func(name string) Middleware {
		return func(next Caller) Caller {
			return callerFunc(func(ctx context.Context, req Request) (Response, error) {
				order = append(order, name)
				return next.Call(ctx, req)
			})
		}
	}
	chained := Chain(base, mw("a"), mw("b"))

	resp, err := chained.Call(context.Background(), Request{Action: "ping"})
	if err != nil {
		t.Fatalf("Call: actual=%v expected=nil", err)
	}
	if !resp.OK() {
		t.Fatalf("response: actual=%+v expected retcode 0", resp)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("middleware order: actual=%v expected=[a b]", order)
	}
	if base.calls.Load() != 1 {
		t.Fatalf("base calls: actual=%d expected=1", base.calls.Load())
	}
}

type callerFunc func(ctx context.Context, req Request) (Response, error)

func (f callerFunc) Call(ctx context.Context, req Request) (Response, error) { return f(ctx, req) }

func Test_F05_ContextTimeoutReturnsWithinBudget(t *testing.T) {
	t.Parallel()
	blocking := callerFunc(func(ctx context.Context, req Request) (Response, error) {
		<-ctx.Done()
		return Response{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := blocking.Call(ctx, Request{Action: "x"})
	if err == nil {
		t.Fatalf("Call: actual=nil expected=context deadline error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Call ignored the deadline: actual=%v", elapsed)
	}
}

func Test_F05_NilParamsAreSafe(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{}}
	if _, err := base.Call(context.Background(), Request{Action: "noop", Params: nil}); err != nil {
		t.Fatalf("Call with nil params: actual=%v expected=nil", err)
	}
	payload, err := json.Marshal(Request{Action: "noop"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(payload) != `{"action":"noop"}` {
		t.Fatalf("nil params should serialize away: actual=%s", payload)
	}
}

func Test_F05_RecordingCallerRecordsSentMessageIDs(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{Data: json.RawMessage("{\"message_id\":12345}")}}
	rec := NewRecordingCaller(base, 4)
	for i := 0; i < 3; i++ {
		if _, err := rec.Call(context.Background(), Request{Action: "send_group_msg"}); err != nil {
			t.Fatalf("Call: %v", err)
		}
	}
	ids := rec.SentIDs()
	if len(ids) != 3 {
		t.Fatalf("recorded IDs: actual=%d expected=3", len(ids))
	}
	if ids[0].Int64() != 12345 {
		t.Fatalf("recorded ID: actual=%d expected=12345", ids[0].Int64())
	}

	// Data 为空时不得 panic，也不记录。
	empty := &RecordingCaller{next: &stubCaller{}, max: 4}
	if _, err := empty.Call(context.Background(), Request{Action: "x"}); err != nil {
		t.Fatalf("Call with empty data: %v", err)
	}
	if got := len(empty.SentIDs()); got != 0 {
		t.Fatalf("recorded from empty data: actual=%d expected=0", got)
	}
}

func Test_F05_RateLimitedCallerRejectsOverBurst(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{}}
	rl := NewRateLimitedCaller(base, 0.0001, 3)
	allowed := 0
	for i := 0; i < 5; i++ {
		if _, err := rl.Call(context.Background(), Request{Action: "x"}); err == nil {
			allowed++
		} else if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed calls: actual=%d expected=3", allowed)
	}
	if base.calls.Load() != 3 {
		t.Fatalf("upstream calls: actual=%d expected=3 (rejected calls must not reach upstream)", base.calls.Load())
	}
}

func Test_F05_RetryCallerRetriesTransportErrorsOnly(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{}, failN: 2}
	rc := NewRetryCaller(base, retry.Policy{
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	})
	if _, err := rc.Call(context.Background(), Request{Action: "x"}); err != nil {
		t.Fatalf("Call: actual=%v expected=nil after retries", err)
	}
	if base.calls.Load() != 3 {
		t.Fatalf("attempts: actual=%d expected=3", base.calls.Load())
	}

	business := &stubCaller{resp: Response{RetCode: 100, Wording: "bad param"}}
	rc2 := NewRetryCaller(business, retry.Policy{
		MaxAttempts: 3,
		Sleep:       func(ctx context.Context, d time.Duration) error { return nil },
	})
	resp, err := rc2.Call(context.Background(), Request{Action: "x"})
	if err != nil {
		t.Fatalf("business error must not be retried or wrapped: actual=%v", err)
	}
	if resp.RetCode != 100 || business.calls.Load() != 1 {
		t.Fatalf("business error handling: actual=(retcode=%d calls=%d) expected=(100,1)", resp.RetCode, business.calls.Load())
	}
}

func Test_F05_SemanticWrappersBuildCorrectActions(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{Data: json.RawMessage("{\"message_id\":9}")}}
	id, err := SendGroupMsg(context.Background(), base, 30003, event.Message{event.Text("hi")})
	if err != nil {
		t.Fatalf("SendGroupMsg: %v", err)
	}
	if id.Int64() != 9 {
		t.Fatalf("returned id: actual=%d expected=9", id.Int64())
	}
	if base.last.Action != "send_group_msg" {
		t.Fatalf("action: actual=%q expected=send_group_msg", base.last.Action)
	}
	if base.last.Params["group_id"] != int64(30003) {
		t.Fatalf("group_id: actual=%v expected=30003", base.last.Params["group_id"])
	}

	if _, err := SendPrivateMsg(context.Background(), base, 20002, event.Message{event.Text("hi")}); err != nil {
		t.Fatalf("SendPrivateMsg: %v", err)
	}
	if base.last.Action != "send_private_msg" {
		t.Fatalf("action: actual=%q expected=send_private_msg", base.last.Action)
	}

	if err := DeleteMsg(context.Background(), base, event.IDFromInt64(9)); err != nil {
		t.Fatalf("DeleteMsg: %v", err)
	}
	if base.last.Action != "delete_msg" {
		t.Fatalf("action: actual=%q expected=delete_msg", base.last.Action)
	}

	if err := SetGroupBan(context.Background(), base, 1, 2, 60*time.Second); err != nil {
		t.Fatalf("SetGroupBan: %v", err)
	}
	if base.last.Action != "set_group_ban" || base.last.Params["duration"] != int64(60) {
		t.Fatalf("set_group_ban params: actual=%+v", base.last)
	}
}

func Test_F05_NonOKResponseSurfacesError(t *testing.T) {
	t.Parallel()
	base := &stubCaller{resp: Response{RetCode: 1, Wording: "nope"}}
	if _, err := SendGroupMsg(context.Background(), base, 1, nil); err == nil {
		t.Fatalf("SendGroupMsg with retcode 1: actual=nil expected=error")
	}
}
