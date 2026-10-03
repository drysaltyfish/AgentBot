package cost

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/testutil"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	const tol = 1e-9
	if diff := got - want; diff > tol || diff < -tol {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
}

func mustNew(t *testing.T, opts Options) *Tracker {
	t.Helper()
	tr, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func knownPrices() PriceTable {
	return PriceTable{Prices: map[string]Price{
		"gpt-x": {InputPer1K: 0.001, OutputPer1K: 0.002},
	}}
}

func Test_F66_PricingAndAggregation(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	tr := mustNew(t, Options{Prices: knownPrices(), Clock: clock})

	ev, err := tr.Record(Call{Provider: "openai", Model: "gpt-x", PromptTokens: 1000, CompletionTokens: 500, SessionKey: "s1", UserID: "u1", Latency: 250 * time.Millisecond})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	approx(t, "event cost", ev.Cost, 0.002)
	if !ev.Timestamp.Equal(clock.Now()) {
		t.Fatalf("timestamp: got %v, want %v", ev.Timestamp, clock.Now())
	}

	g := tr.Global()
	if g.Calls != 1 || g.PromptTokens != 1000 || g.CompletionTokens != 500 || g.TotalTokens() != 1500 {
		t.Fatalf("global: %+v", g)
	}
	approx(t, "global cost", g.Cost, 0.002)
	if s := tr.Session("s1"); s.Calls != 1 {
		t.Fatalf("session: %+v", s)
	}
	if u := tr.User("u1"); u.Calls != 1 {
		t.Fatalf("user: %+v", u)
	}
	if d := tr.Today(); d.Calls != 1 {
		t.Fatalf("today: %+v", d)
	}
	if m := tr.Month(); m.Calls != 1 {
		t.Fatalf("month: %+v", m)
	}
	if other := tr.Session("s2"); other.Calls != 0 {
		t.Fatalf("unexpected session s2: %+v", other)
	}
}

func Test_F66_UnknownModelWarnZero(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Warn: func(m string) {
			mu.Lock()
			warns = append(warns, m)
			mu.Unlock()
		},
	})
	ev, err := tr.Record(Call{Model: "mystery", PromptTokens: 1000, CompletionTokens: 1000})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	approx(t, "unknown cost", ev.Cost, 0)
	if ev.Model != "mystery" {
		t.Fatalf("event model: %q", ev.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warns) != 1 {
		t.Fatalf("warns: got %v, want one unknown-model warning", warns)
	}
}

func Test_F66_UnknownModelReject(t *testing.T) {
	tr := mustNew(t, Options{
		Prices: PriceTable{Prices: knownPrices().Prices, Unknown: UnknownReject},
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
	})
	if _, err := tr.Record(Call{Model: "mystery", PromptTokens: 1}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Record: got %v, want ErrUnknownModel", err)
	}
	if g := tr.Global(); g.Calls != 0 {
		t.Fatalf("rejected record must not aggregate: %+v", g)
	}
}

func Test_F66_DayLimitDenyAndReset(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 1, 15, 23, 0, 0, 0, time.UTC))
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  clock,
		Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodDay, Limit: 0.01, Action: ActionDeny}},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 10_000}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dec, err := tr.Authorize("", "")
	var qerr *QuotaError
	if !errors.As(err, &qerr) {
		t.Fatalf("Authorize: got %v, want *QuotaError", err)
	}
	if dec.Allowed || dec.Action != ActionDeny || dec.Scope != ScopeGlobal || dec.Period != PeriodDay {
		t.Fatalf("decision: %+v", dec)
	}
	clock.Set(time.Date(2026, 1, 16, 0, 1, 0, 0, time.UTC))
	if dec, err := tr.Authorize("", ""); err != nil || !dec.Allowed {
		t.Fatalf("next day should reset: dec=%+v err=%v", dec, err)
	}
}

func Test_F66_MonthLimitAndReset(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC))
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  clock,
		Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodMonth, Limit: 0.01, Action: ActionDeny}},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 10_000}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := tr.Authorize("", ""); err == nil {
		t.Fatalf("Authorize: want denial in same month")
	}
	clock.Set(time.Date(2026, 2, 1, 0, 0, 1, 0, time.UTC))
	if dec, err := tr.Authorize("", ""); err != nil || !dec.Allowed {
		t.Fatalf("next month should reset: dec=%+v err=%v", dec, err)
	}
}

func Test_F66_SessionLimitIsolated(t *testing.T) {
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Quotas: []Quota{{Scope: ScopeSession, Period: PeriodTotal, Limit: 0.01, Action: ActionDeny}},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 10_000, SessionKey: "a"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := tr.Authorize("a", ""); err == nil {
		t.Fatalf("session a should be denied")
	}
	if dec, err := tr.Authorize("b", ""); err != nil || !dec.Allowed {
		t.Fatalf("session b should be unaffected: dec=%+v err=%v", dec, err)
	}
}

func Test_F66_SoftHardWarningsDedup(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodTotal, Limit: 0.01, SoftLimit: 0.008, Action: ActionWarn}},
		Warn: func(m string) {
			mu.Lock()
			warns = append(warns, m)
			mu.Unlock()
		},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 9000}); err != nil {
		t.Fatalf("Record soft: %v", err)
	}
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 9000}); err != nil {
		t.Fatalf("Record hard: %v", err)
	}
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 9000}); err != nil {
		t.Fatalf("Record again: %v", err)
	}
	dec, err := tr.Authorize("", "")
	if err != nil || !dec.Allowed || !dec.SoftLimit {
		t.Fatalf("warn action must allow: dec=%+v err=%v", dec, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warns) != 2 {
		t.Fatalf("warns: got %d %v, want exactly soft+hard", len(warns), warns)
	}
}

func Test_F66_DowngradeAction(t *testing.T) {
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodTotal, Limit: 0.01, Action: ActionDowngrade, DowngradeModel: "cheap"}},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 10_000}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dec, err := tr.Authorize("", "")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Allowed || dec.Action != ActionDowngrade || dec.DowngradeModel != "cheap" {
		t.Fatalf("decision: %+v", dec)
	}
}

func Test_F66_DefaultActionIsDeny(t *testing.T) {
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodTotal, Limit: 0.01}},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 10_000}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dec, err := tr.Authorize("", "")
	if err == nil || dec.Allowed || dec.Action != ActionDeny {
		t.Fatalf("empty action should default to deny: dec=%+v err=%v", dec, err)
	}
}

func Test_F66_InvalidConfig(t *testing.T) {
	if _, err := New(Options{Quotas: []Quota{{Scope: ScopeGlobal, Period: PeriodDay, Limit: 0.01, Action: ActionDowngrade}}}); err == nil {
		t.Fatalf("downgrade without model should fail")
	}
	if _, err := New(Options{Quotas: []Quota{{Scope: "bogus", Period: PeriodDay, Limit: 0.01, Action: ActionDeny}}}); err == nil {
		t.Fatalf("invalid scope should fail")
	}
	tr := mustNew(t, Options{Clock: testutil.NewFakeClock(time.Unix(0, 0).UTC())})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: -1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("negative tokens: got %v, want ErrInvalidUsage", err)
	}
}

func Test_F66_ConcurrentRecord(t *testing.T) {
	tr := mustNew(t, Options{
		Prices: PriceTable{Prices: map[string]Price{"gpt-x": {InputPer1K: 0.001}}},
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
	})
	const workers = 100
	const perWorker = 10
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 100, SessionKey: fmt.Sprintf("s%d", n)}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Record: %v", err)
	}
	g := tr.Global()
	if g.Calls != workers*perWorker || g.PromptTokens != workers*perWorker*100 {
		t.Fatalf("global: %+v", g)
	}
	approx(t, "global cost", g.Cost, 0.1)
	if len(tr.Snapshot().Buckets) == 0 {
		t.Fatalf("snapshot empty")
	}
}

type fakeStore struct {
	mu    sync.Mutex
	snap  *Snapshot
	saves int
	loads int
}

func (s *fakeStore) Load() (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.snap, nil
}

func (s *fakeStore) Save(snap *Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *snap
	cp.Buckets = append([]Bucket(nil), snap.Buckets...)
	s.snap = &cp
	s.saves++
	return nil
}

func Test_F66_PersistenceRoundTrip(t *testing.T) {
	store := &fakeStore{}
	clock := testutil.NewFakeClock(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	tr := mustNew(t, Options{Prices: knownPrices(), Clock: clock, Store: store})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 1000, SessionKey: "s1"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Record after close: got %v, want ErrClosed", err)
	}
	store.mu.Lock()
	saves := store.saves
	store.mu.Unlock()
	if saves == 0 {
		t.Fatalf("store never saved")
	}
	tr2 := mustNew(t, Options{Prices: knownPrices(), Clock: clock, Store: store})
	defer func() { _ = tr2.Close() }()
	if g := tr2.Global(); g.Calls != 1 || g.PromptTokens != 1000 {
		t.Fatalf("restored global: %+v", g)
	}
	approx(t, "restored cost", tr2.Global().Cost, 0.001)
}

func Test_F66_MetricsSink(t *testing.T) {
	var mu sync.Mutex
	metrics := map[string]float64{}
	tr := mustNew(t, Options{
		Prices: knownPrices(),
		Clock:  testutil.NewFakeClock(time.Unix(0, 0).UTC()),
		Metric: func(name string, delta float64) {
			mu.Lock()
			metrics[name] += delta
			mu.Unlock()
		},
	})
	if _, err := tr.Record(Call{Model: "gpt-x", PromptTokens: 1000, CompletionTokens: 0}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := tr.Record(Call{Model: "mystery", PromptTokens: 5}); err != nil {
		t.Fatalf("Record unknown: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	approx(t, "cost_usd_total", metrics["cost_usd_total"], 0.001)
	approx(t, "cost_tokens_total", metrics["cost_tokens_total"], 1005)
	approx(t, "cost_unknown_model_total", metrics["cost_unknown_model_total"], 1)
}
