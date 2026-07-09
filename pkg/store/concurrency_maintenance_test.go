package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// holderExists reports whether a holder row is present for (key, holderID),
// regardless of lease state. Used to confirm a reaped holder's row is gone.
func holderExists(t *testing.T, s *store.Store, key, holderID string) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&n); err != nil {
		t.Fatalf("count holder: %v", err)
	}
	return n > 0
}

func waiterCount(t *testing.T, s *store.Store, key string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ?`, key,
	).Scan(&n); err != nil {
		t.Fatalf("count waiters: %v", err)
	}
	return n
}

func seedCacheRow(t *testing.T, s *store.Store, key, hash string, expiresAt, lastHitAt time.Time) {
	t.Helper()
	now := time.Now()
	if _, err := s.DB().Exec(
		`INSERT INTO concurrency_cache
		   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key, hash, "out", "r0", "n0", now.UnixNano(), expiresAt.UnixNano(), lastHitAt.UnixNano(),
	); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
}

// A controllerless box never runs the reaper, so the maintenance pass is
// the only thing that reclaims an expired holder's budget. It must delete
// the dead holder and promote the waiter parked behind it.
func TestMaintainConcurrency_ReapsExpiredHolderAndPromotesWaiter(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: 40 * time.Millisecond,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued, got %s", r.Kind)
	}
	time.Sleep(80 * time.Millisecond)

	res, err := s.MaintainConcurrency(ctxT(t), store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if holderExists(t, s, "k", "rA/n") {
		t.Fatalf("expired holder rA/n still present after sweep")
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders = %d, want 1 (B promoted into freed slot)", got)
	}
	if !holderExists(t, s, "k", "rB/n") {
		t.Fatalf("waiter rB/n was not promoted into a holder")
	}
	if res.Reconciled == 0 && res.Promoted == 0 {
		t.Fatalf("result reported no promotion; reconciled=%d promoted=%d", res.Reconciled, res.Promoted)
	}
}

// Expired cache rows accumulate forever on a daemonless box; the TTL sweep
// must clear them.
func TestMaintainConcurrency_SweepsExpiredCacheRows(t *testing.T) {
	s := newStoreT(t)
	past := time.Now().Add(-time.Hour)
	seedCacheRow(t, s, "memo:k", "h1", past, past)

	res, err := s.MaintainConcurrency(ctxT(t), store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if res.CacheExpired != 1 {
		t.Fatalf("CacheExpired = %d, want 1", res.CacheExpired)
	}
	if n, err := s.CountConcurrencyCache(ctxT(t)); err != nil {
		t.Fatalf("count cache: %v", err)
	} else if n != 0 {
		t.Fatalf("cache rows = %d, want 0 after TTL sweep", n)
	}
}

// Unbounded cache growth is the core symptom; the LRU sweep must bound the
// table to the configured cap, evicting the least-recently-hit rows.
func TestMaintainConcurrency_EvictsOverCapCacheRows(t *testing.T) {
	s := newStoreT(t)
	future := time.Now().Add(time.Hour)
	base := time.Now().Add(-time.Hour)
	for i := range 5 {
		seedCacheRow(t, s, "memo:k", string(rune('a'+i)), future, base.Add(time.Duration(i)*time.Minute))
	}

	res, err := s.MaintainConcurrency(ctxT(t), store.ConcurrencyMaintenanceOptions{CacheCap: 2})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if res.CacheEvicted != 3 {
		t.Fatalf("CacheEvicted = %d, want 3 (5 rows down to cap 2)", res.CacheEvicted)
	}
	if n, err := s.CountConcurrencyCache(ctxT(t)); err != nil {
		t.Fatalf("count cache: %v", err)
	} else if n != 2 {
		t.Fatalf("cache rows = %d, want 2 (cap)", n)
	}
}

// Finished and abandoned runs leave waiter rows behind; the age sweep must
// drop any waiter older than the configured max age.
func TestMaintainConcurrency_DropsAgedWaiter(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued, got %s", r.Kind)
	}

	res, err := s.MaintainConcurrency(ctxT(t), store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Nanosecond})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleWaiters) != 1 {
		t.Fatalf("StaleWaiters = %d, want 1", len(res.StaleWaiters))
	}
	if got := waiterCount(t, s, "k"); got != 0 {
		t.Fatalf("waiters = %d, want 0 after age sweep", got)
	}
}

// The inline run-path pass must fire at most once per interval across
// separate processes; the throttle claims the window atomically.
func TestMaintainConcurrencyThrottled_RespectsInterval(t *testing.T) {
	s := newStoreT(t)

	if _, ran, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, time.Hour); err != nil {
		t.Fatalf("first throttled pass: %v", err)
	} else if !ran {
		t.Fatalf("first pass did not run; the window should have been free")
	}

	if _, ran, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, time.Hour); err != nil {
		t.Fatalf("second throttled pass: %v", err)
	} else if ran {
		t.Fatalf("second pass ran inside the interval; throttle did not hold")
	}

	if _, ran, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, 0); err != nil {
		t.Fatalf("zero-interval pass: %v", err)
	} else if !ran {
		t.Fatalf("zero interval should always claim the window")
	}
}

func TestMaintainConcurrencyThrottled_InProgressClaimSuppressesStampede(t *testing.T) {
	s := newStoreT(t)
	nowNS := time.Now().UnixNano()
	if _, err := s.DB().Exec(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)`,
		"concurrency_sweep_claimed_at", nowNS, nowNS,
	); err != nil {
		t.Fatalf("insert claim: %v", err)
	}
	if _, ran, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, time.Hour); err != nil {
		t.Fatalf("claimed throttled pass: %v", err)
	} else if ran {
		t.Fatalf("pass ran while another process held the in-progress claim")
	}

	oldNS := time.Now().Add(-time.Hour).UnixNano()
	if _, err := s.DB().Exec(
		`UPDATE sparkwing_meta SET value = ?, updated_at = ? WHERE key = ?`,
		oldNS, oldNS, "concurrency_sweep_claimed_at",
	); err != nil {
		t.Fatalf("expire claim: %v", err)
	}
	if _, ran, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, time.Hour); err != nil {
		t.Fatalf("expired-claim throttled pass: %v", err)
	} else if !ran {
		t.Fatalf("expired claim should allow the next caller to retry")
	}
}

func TestMaintainConcurrencyThrottled_ConcurrentCallersShareOneClaim(t *testing.T) {
	s := newStoreT(t)
	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	ran := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, didRun, err := s.MaintainConcurrencyThrottled(ctxT(t), store.ConcurrencyMaintenanceOptions{}, time.Hour)
			if err != nil {
				t.Errorf("MaintainConcurrencyThrottled: %v", err)
				return
			}
			ran <- didRun
		}()
	}
	close(start)
	wg.Wait()
	close(ran)
	count := 0
	for didRun := range ran {
		if didRun {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ran count = %d, want 1", count)
	}
}

// Migration v4 adds the meta table the throttle stamp lives in; a fresh
// open must create it.
func TestMigration_V4CreatesMetaTable(t *testing.T) {
	s := newStoreT(t)
	if store.ExpectedSchemaVersion() < 4 {
		t.Fatalf("ExpectedSchemaVersion = %d, want >= 4", store.ExpectedSchemaVersion())
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sparkwing_meta`).Scan(&n); err != nil {
		t.Fatalf("sparkwing_meta not queryable after open: %v", err)
	}
}
