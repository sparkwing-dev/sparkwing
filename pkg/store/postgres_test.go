package store_test

import (
	"context"

	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func openPGTestStore(t *testing.T) *store.Store {
	t.Helper()
	return storetest.OpenPostgres(t)
}

func TestPostgresOpenAndMigrate(t *testing.T) {
	st := openPGTestStore(t)
	if got, want := st.Dialect(), store.DialectPostgres; got != want {
		t.Errorf("Dialect = %v, want %v", got, want)
	}
	ctx := context.Background()
	r := store.Run{
		ID:        "pg-open-test",
		Pipeline:  "p",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := st.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != r.ID {
		t.Fatalf("GetRun returned %+v, want id %q", got, r.ID)
	}
}

func TestPostgresAcquireConcurrencySlot_Serializes(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	const key = "shared-slot"
	const n = 5

	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("r-%d", i)
		if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
			ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", runID, err)
		}
	}

	type result struct {
		kind store.AcquireKind
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := tenantFor(t, st, store.DefaultTeam).AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
				Key:      key,
				RunID:    fmt.Sprintf("r-%d", i),
				NodeID:   "n",
				HolderID: fmt.Sprintf("h-%d", i),
				Capacity: 1,
				Policy:   store.OnLimitQueue,
				Lease:    time.Minute,
			})
			results[i] = result{kind: resp.Kind, err: err}
		}(i)
	}
	wg.Wait()

	var granted, queued int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("unexpected error: %v", r.err)
			continue
		}
		switch r.kind {
		case store.AcquireGranted:
			granted++
		case store.AcquireQueued:
			queued++
		default:
			t.Errorf("unexpected acquire kind: %v", r.kind)
		}
	}
	if granted != 1 {
		t.Errorf("granted = %d, want 1", granted)
	}
	if queued != n-1 {
		t.Errorf("queued = %d, want %d", queued, n-1)
	}
}

func TestPostgresResolveWaiterPromotesOnceUnderConcurrentPoll(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	acquireT(t, st, store.AcquireSlotRequest{
		Key: "resolve-slot", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	for i := 0; i < 2; i++ {
		acquireT(t, st, store.AcquireSlotRequest{
			Key: "resolve-slot", HolderID: fmt.Sprintf("w-%d", i), RunID: fmt.Sprintf("waiter-%d", i), NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue,
		})
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = $1 AND holder_id = $2`,
		"resolve-slot", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	type result struct {
		resolution store.WaiterResolution
		err        error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolution, err := tenantFor(t, st, store.DefaultTeam).ResolveWaiter(ctx, "resolve-slot", fmt.Sprintf("waiter-%d", i), "n", "", "", "", false)
			results[i] = result{resolution: resolution, err: err}
		}(i)
	}
	wg.Wait()

	var promoted, waiting int
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("resolve error: %v", result.err)
		}
		switch result.resolution.Status {
		case store.WaiterPromoted:
			promoted++
		case store.WaiterStillWaiting:
			waiting++
		default:
			t.Fatalf("unexpected resolution: %+v", result.resolution)
		}
	}
	if promoted != 1 || waiting != 1 {
		t.Fatalf("promoted=%d waiting=%d, want promoted=1 waiting=1", promoted, waiting)
	}
	state, err := tenantFor(t, st, store.DefaultTeam).GetConcurrencyState(ctx, "resolve-slot")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter-0" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "waiter-1" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}
