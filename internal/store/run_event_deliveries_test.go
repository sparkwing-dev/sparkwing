package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type deliveryFixture struct {
	s   *store.Store
	now *time.Time
	mu  *sync.Mutex
}

func (f deliveryFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.now = f.now.Add(d)
}

func (f deliveryFixture) at() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.now
}

func newDeliveryFixture(t *testing.T, events int) deliveryFixture {
	t.Helper()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := storetest.Open(t, teststore.WithClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now }))
	ctx := t.Context()
	team := store.DefaultTeam
	if err := teststore.SeedRun(s, ctx, team, store.Run{RunSummary: runstate.RunSummary{ID: "run", Pipeline: "p", Status: "success", StartedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRunEventSubscription(ctx, s, team, "sub_a", "https://hooks.example.test/a", "sealed", runevents.Filter{EventTypes: []string{runevents.EventFinished}}); err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if err := teststore.SeedRunEventDelivery(ctx, s, team, "sub_a", runevents.ID("run", int64(i+1)), "run", []byte(`{"n":1}`), now); err != nil {
			t.Fatal(err)
		}
	}
	return deliveryFixture{s: s, now: &now, mu: &mu}
}

func (f deliveryFixture) rows(t *testing.T) map[string]teststore.RunEventDeliveryRow {
	t.Helper()
	out := map[string]teststore.RunEventDeliveryRow{}
	for _, r := range deliveriesOf(t, f.s, store.DefaultTeam) {
		out[r.EventID] = r
	}
	return out
}

func TestRunEventDeliveryClaimLeaseAndFencedFinish(t *testing.T) {
	f := newDeliveryFixture(t, 1)
	ctx := t.Context()
	op := f.s.AsOperator()
	claimed, err := op.ClaimRunEventDeliveries(ctx, 8)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %+v, %v", claimed, err)
	}
	first := claimed[0]
	if first.Attempts != 1 || first.Subscription.URL != "https://hooks.example.test/a" || first.Subscription.Secret != "sealed" || string(first.Body) != `{"n":1}` || first.Team != store.DefaultTeam {
		t.Fatalf("claimed %+v", first)
	}
	if again, err := op.ClaimRunEventDeliveries(ctx, 8); err != nil || len(again) != 0 {
		t.Fatalf("leased row claimed again: %+v, %v", again, err)
	}
	f.advance(store.RunEventDeliveryLease)
	claimed, err = op.ClaimRunEventDeliveries(ctx, 8)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("expired lease reclaim %+v, %v", claimed, err)
	}
	if ok, err := op.FinishRunEventDelivery(ctx, first, store.RunEventOutcome{Delivered: true, Status: 200}); err != nil || ok {
		t.Fatalf("stale sender's outcome applied: %v, %v", ok, err)
	}
	if ok, err := op.FinishRunEventDelivery(ctx, claimed[0], store.RunEventOutcome{Status: 503, Code: "http_status", Delay: 30 * time.Second}); err != nil || !ok {
		t.Fatalf("current outcome refused: %v, %v", ok, err)
	}
	row := f.rows(t)["run/1"]
	if row.State != "pending" || row.Attempts != 2 || !row.NextAt.Equal(f.at().Add(30*time.Second)) || row.LastStatus.Int64 != 503 || row.LastError != "http_status" || row.Finished {
		t.Fatalf("retry row %+v", row)
	}
	if early, err := op.ClaimRunEventDeliveries(ctx, 8); err != nil || len(early) != 0 {
		t.Fatalf("claimed before next_at: %+v, %v", early, err)
	}
	f.advance(30 * time.Second)
	claimed, err = op.ClaimRunEventDeliveries(ctx, 8)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("due retry %+v, %v", claimed, err)
	}
	if ok, err := op.FinishRunEventDelivery(ctx, claimed[0], store.RunEventOutcome{Delivered: true, Status: 204}); err != nil || !ok {
		t.Fatalf("success refused: %v, %v", ok, err)
	}
	row = f.rows(t)["run/1"]
	if row.State != "succeeded" || row.Attempts != 3 || row.LastStatus.Int64 != 204 || row.LastError != "" || !row.Finished {
		t.Fatalf("delivered row %+v", row)
	}
	f.advance(48 * time.Hour)
	if after, err := op.ClaimRunEventDeliveries(ctx, 8); err != nil || len(after) != 0 {
		t.Fatalf("succeeded row claimed: %+v, %v", after, err)
	}
}

func TestRunEventDeliveryFailsAtTheDeadline(t *testing.T) {
	f := newDeliveryFixture(t, 2)
	ctx := t.Context()
	op := f.s.AsOperator()
	claimed, err := op.ClaimRunEventDeliveries(ctx, 8)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim %+v, %v", claimed, err)
	}
	if err := teststore.SeedRunEventDelivery(ctx, f.s, store.DefaultTeam, "sub_a", "run/9", "run", []byte(`{}`), f.at()); err != nil {
		t.Fatal(err)
	}
	f.advance(store.RunEventDeliveryWindow - time.Minute)
	if ok, err := op.FinishRunEventDelivery(ctx, claimed[0], store.RunEventOutcome{Status: 500, Code: "http_status", Delay: 2 * time.Minute}); err != nil || !ok {
		t.Fatalf("finish %v, %v", ok, err)
	}
	f.advance(time.Minute)
	if again, err := op.ClaimRunEventDeliveries(ctx, 8); err != nil || len(again) != 0 {
		t.Fatalf("claimed past deadline: %+v, %v", again, err)
	}
	rows := f.rows(t)
	if r := rows["run/1"]; r.State != "failed" || r.LastStatus.Int64 != 500 || r.LastError != "http_status" || !r.Finished {
		t.Fatalf("retry past deadline %+v", r)
	}
	if r := rows["run/2"]; r.State != "failed" || r.LastError != "timeout" || !r.Finished {
		t.Fatalf("abandoned lease past deadline %+v", r)
	}
	if r := rows["run/9"]; r.State != "failed" || r.Attempts != 0 || r.LastError != "expired" || !r.Finished {
		t.Fatalf("never-attempted delivery past deadline %+v", r)
	}
}

func TestTwoRunEventSendersNeverClaimTheSameDelivery(t *testing.T) {
	const events = 24
	f := newDeliveryFixture(t, events)
	ctx := t.Context()
	senders := []*store.Operator{f.s.AsOperator(), f.s.AsOperator()}
	start := make(chan struct{})
	results := make([][]store.RunEventDelivery, len(senders))
	errs := make([]error, len(senders))
	var wg sync.WaitGroup
	for i, op := range senders {
		wg.Go(func() {
			<-start
			for {
				batch, err := op.ClaimRunEventDeliveries(ctx, 3)
				if err != nil {
					errs[i] = err
					return
				}
				if len(batch) == 0 {
					return
				}
				results[i] = append(results[i], batch...)
			}
		})
	}
	close(start)
	wg.Wait()
	seen := map[string]int{}
	for i := range senders {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		for _, d := range results[i] {
			seen[d.EventID]++
		}
	}
	if len(seen) != events {
		t.Fatalf("claimed %d of %d deliveries", len(seen), events)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s claimed %d times", id, n)
		}
	}
}
