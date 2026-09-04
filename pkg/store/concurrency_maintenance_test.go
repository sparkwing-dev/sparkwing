package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func expireHolderLease(t *testing.T, s *store.Store, key, holderID string) {
	t.Helper()
	result, err := s.DB().Exec(
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
		time.Now().Add(-time.Minute).UnixNano(), key, holderID,
	)
	if err != nil {
		t.Fatalf("expire holder lease: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("expire holder lease rows affected: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expire holder lease matched %d rows, want 1", rows)
	}
}

func seedCacheRow(t *testing.T, s *store.Store, key, hash string, expiresAt, lastHitAt time.Time) {
	t.Helper()
	createLiveRunT(t, s, "r0")
	seedCacheRowFor(t, s, key, hash, "r0", expiresAt, lastHitAt)
}

func seedCacheRowFor(t *testing.T, s *store.Store, key, hash, originRun string, expiresAt, lastHitAt time.Time) {
	t.Helper()
	now := time.Now()
	if _, err := s.DB().Exec(
		`INSERT INTO concurrency_cache
		   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key, hash, "out", originRun, "n0", now.UnixNano(), expiresAt.UnixNano(), lastHitAt.UnixNano(),
	); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
}

func TestMaintainConcurrency_ReapsExpiredHolderAndPromotesWaiter(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued, got %s", r.Kind)
	}
	createLiveRunT(t, s, "rB")
	expireHolderLease(t, s, "k", "rA/n")

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

func TestMaintainConcurrency_DropsAgedWaiter(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireBareT(t, s, store.AcquireSlotRequest{
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
	if holderExists(t, s, "k", "rB/n") {
		t.Fatalf("abandoned waiter was promoted into a holder")
	}
}

func TestMaintainConcurrency_DoesNotPromoteAbandonedWaiterAfterHolderReap(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder/-", RunID: "holder", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued/-", RunID: "queued", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}
	if _, err := s.DB().Exec(
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ? AND node_id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), "k", "queued", "",
	); err != nil {
		t.Fatalf("age waiter: %v", err)
	}
	expireHolderLease(t, s, "k", "holder/-")

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Millisecond})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleHolders) != 1 {
		t.Fatalf("StaleHolders = %d, want 1", len(res.StaleHolders))
	}
	if holderExists(t, s, "k", "queued/-") {
		t.Fatalf("abandoned waiter was promoted after holder reap")
	}
	if got := waiterCount(t, s, "k"); got != 0 {
		t.Fatalf("waiters = %d, want abandoned waiter dropped", got)
	}
}

func TestMaintainConcurrency_DropsAgedWaiterForTerminalRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder/-", RunID: "holder", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued/-", RunID: "queued", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        "queued",
		Pipeline:  "queued-plan",
		Status:    "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.FinishRun(ctx, "queued", "failed", "test terminal run"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ? AND node_id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), "k", "queued", "",
	); err != nil {
		t.Fatalf("age waiter: %v", err)
	}

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Millisecond})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleWaiters) > 1 {
		t.Fatalf("StaleWaiters = %d, want at most 1 for terminal run", len(res.StaleWaiters))
	}
	if got := waiterCount(t, s, "k"); got != 0 {
		t.Fatalf("waiters = %d, want terminal waiter dropped", got)
	}
	if holderExists(t, s, "k", "queued/-") {
		t.Fatalf("terminal waiter was promoted into a holder")
	}
}

func TestMaintainConcurrency_PreservesAgedWaiterForLiveRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder/-", RunID: "holder", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued/-", RunID: "queued", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        "queued",
		Pipeline:  "queued-plan",
		Status:    "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.TouchRunHeartbeat(ctx, "queued"); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ? AND node_id = ?`,
		time.Now().Add(-10*time.Minute).UnixNano(), "k", "queued", "",
	); err != nil {
		t.Fatalf("age waiter: %v", err)
	}

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Millisecond})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleWaiters) != 0 {
		t.Fatalf("StaleWaiters = %d, want 0 for live run", len(res.StaleWaiters))
	}
	if got := waiterCount(t, s, "k"); got != 1 {
		t.Fatalf("waiters = %d, want live waiter retained", got)
	}
	resolution, err := s.ResolveWaiter(ctx, "k", "queued", "", "", "", "", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterStillWaiting {
		t.Fatalf("resolution = %s, want still_waiting", resolution.Status)
	}
}

func TestMaintainConcurrency_DropsAgedWaiterForStaleRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder/-", RunID: "holder", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued/-", RunID: "queued", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        "queued",
		Pipeline:  "queued-plan",
		Status:    "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	stale := time.Now().Add(-10 * time.Minute).UnixNano()
	if _, err := s.DB().Exec(
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		stale, "queued",
	); err != nil {
		t.Fatalf("stale run heartbeat: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ? AND node_id = ?`,
		stale, "k", "queued", "",
	); err != nil {
		t.Fatalf("age waiter: %v", err)
	}

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Millisecond})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleWaiters) != 1 {
		t.Fatalf("StaleWaiters = %d, want 1 for stale run", len(res.StaleWaiters))
	}
	if got := waiterCount(t, s, "k"); got != 0 {
		t.Fatalf("waiters = %d, want stale waiter dropped", got)
	}
	if holderExists(t, s, "k", "queued/-") {
		t.Fatalf("stale waiter was promoted into a holder")
	}
}

func TestMaintainConcurrency_DropsAgedWaiterWhenHeartbeatGraceExpiresBeforeWaiterAge(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder/-", RunID: "holder", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued/-", RunID: "queued", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID:        "queued",
		Pipeline:  "queued-plan",
		Status:    "running",
		StartedAt: time.Now().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	staleHeartbeat := time.Now().Add(-(store.DefaultLeaseDuration + time.Minute)).UnixNano()
	if _, err := s.DB().Exec(
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		staleHeartbeat, "queued",
	); err != nil {
		t.Fatalf("stale run heartbeat: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ? AND node_id = ?`,
		time.Now().Add(-20*time.Minute).UnixNano(), "k", "queued", "",
	); err != nil {
		t.Fatalf("age waiter: %v", err)
	}

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: 10 * time.Minute})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if len(res.StaleWaiters) != 1 {
		t.Fatalf("StaleWaiters = %d, want 1 for stale heartbeat", len(res.StaleWaiters))
	}
	if got := waiterCount(t, s, "k"); got != 0 {
		t.Fatalf("waiters = %d, want stale-heartbeat waiter dropped", got)
	}
	if holderExists(t, s, "k", "queued/-") {
		t.Fatalf("stale-heartbeat waiter was promoted into a holder")
	}
}

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

// A memo entry whose origin run is gone can only fail the node that hits
// it, and entries written before deletes cleaned them up outlive the run
// by their whole TTL.
func TestMaintainConcurrency_SweepsCacheRowsWhoseOriginRunIsGone(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	future := time.Now().Add(time.Hour)
	createLiveRunT(t, s, "live")
	seedCacheRowFor(t, s, "memo:live", "h1", "live", future, time.Now())
	seedCacheRowFor(t, s, "memo:gone", "h2", "gone", future, time.Now())

	res, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		t.Fatalf("MaintainConcurrency: %v", err)
	}
	if res.CacheOrphaned != 1 {
		t.Fatalf("CacheOrphaned = %d, want 1", res.CacheOrphaned)
	}
	if n, err := s.CountConcurrencyCache(ctx); err != nil {
		t.Fatalf("count cache: %v", err)
	} else if n != 1 {
		t.Fatalf("cache rows = %d, want 1 (the live run's entry kept)", n)
	}

	again, err := s.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		t.Fatalf("MaintainConcurrency (second pass): %v", err)
	}
	if again.CacheOrphaned != 0 {
		t.Fatalf("second pass CacheOrphaned = %d, want 0", again.CacheOrphaned)
	}
	if n, err := s.CountConcurrencyCache(ctx); err != nil {
		t.Fatalf("count cache: %v", err)
	} else if n != 1 {
		t.Fatalf("cache rows after second pass = %d, want 1", n)
	}
}
