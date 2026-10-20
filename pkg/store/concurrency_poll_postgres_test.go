package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A queued job's poll used to wait for the entry lock behind every other poll,
// so a release queued behind all of them and handoffs slowed as waiters grew
// (TestConcurrency_BurstConcurrentAcquireAndRelease missed its deadline).
func TestPostgresWaiterPollDoesNotQueueBehindEntryWriter(t *testing.T) {
	s := storetest.NewPostgres(t).Open(t)
	ctx := ctxT(t)
	for _, run := range []string{"r0", "r1"} {
		createLiveRunT(t, s, run)
	}
	tenant := tenantFor(t, s, store.DefaultTeam)
	for i, run := range []string{"r0", "r1"} {
		resp, err := tenant.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{Key: "k", HolderID: "h-" + run, RunID: run, NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
		if err != nil || (i == 0) != (resp.Kind == store.AcquireGranted) {
			t.Fatalf("arrival %s: %+v %v", run, resp, err)
		}
	}
	writer, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Rollback() })
	if _, err := writer.ExecContext(ctx, `SELECT 1 FROM concurrency_entries WHERE team=$1 AND key='k' FOR UPDATE`, store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	poll, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := tenant.ResolveWaiter(poll, "k", "r1", "n", "", "", "", false)
	if err != nil || res.Status != store.WaiterStillWaiting || res.Position != 0 || len(res.Holders) != 1 {
		t.Fatalf("poll behind a busy entry = %+v, %v", res, err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, _, promoted, err := tenant.ReleaseAndNotify(ctx, "k", "h-r0", "success", "", "", 0, store.DefaultConcurrencyLease); err != nil || len(promoted) != 1 {
		t.Fatalf("release: %v %v", promoted, err)
	}
	if res, err := tenant.ResolveWaiter(ctx, "k", "r1", "n", "", "", "", false); err != nil || res.Status != store.WaiterPromoted {
		t.Fatalf("poll after release = %+v, %v", res, err)
	}
}
