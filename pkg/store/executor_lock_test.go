package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCanonicalExecutorNamesOrdersAndDeduplicates(t *testing.T) {
	got := canonicalExecutorNames("zeta", "", "alpha", "zeta", "middle", "alpha")
	want := []string{"alpha", "middle", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical executor names = %v, want %v", got, want)
	}
}

func TestMarkNodeReadyIncludesEligibilityCommittedBeforeItsSnapshot(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	low := ClaimIdentity{Principal: "low-principal", TokenPrefix: "swr_low"}
	high := ClaimIdentity{Principal: "high-principal", TokenPrefix: "swr_high"}
	for _, executor := range []struct {
		identity ClaimIdentity
		name     string
		priority int
	}{
		{identity: low, name: "low", priority: 20},
		{identity: high, name: "high", priority: 80},
	} {
		if err := st.EnrollExecutor(ctx, executor.identity.TokenPrefix, Executor{
			Name: executor.name, Kind: "agent", Location: "local", Principal: executor.identity.Principal,
			BasePriority: executor.priority, PriorityCeiling: executor.priority, MaxConcurrent: 1,
			Budget: ExecutorResource{Cores: 4},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.HeartbeatExecutor(ctx, low, "low", ExecutorResource{Cores: 4}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	eligibilityWriter, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eligibilityWriter.ExecContext(ctx, `UPDATE executors
   SET last_seen = ?, headroom_reported = 1, headroom_cores = 4
 WHERE name = 'high'`, time.Now().UnixNano()); err != nil {
		_ = eligibilityWriter.Rollback()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.MarkNodeReady(ctx, "run", "work") }()
	select {
	case err := <-done:
		_ = eligibilityWriter.Rollback()
		t.Fatalf("MarkNodeReady passed an uncommitted eligibility writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := eligibilityWriter.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MarkNodeReady did not resume after eligibility committed")
	}
	node, err := st.GetNode(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if node.OfferPriorityTarget != 80 {
		t.Fatalf("offer priority target = %d, want newly eligible priority 80", node.OfferPriorityTarget)
	}
}

func TestMarkNodeReadyIncludesActiveSlotReleaseCommittedBeforeItsSnapshot(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for _, executor := range []struct {
		name     string
		priority int
	}{
		{name: "low", priority: 20},
		{name: "high", priority: 80},
	} {
		identity := ClaimIdentity{Principal: executor.name + "-principal", TokenPrefix: "swr_" + executor.name}
		if err := st.EnrollExecutor(ctx, identity.TokenPrefix, Executor{
			Name: executor.name, Kind: "agent", Location: "local", Principal: identity.Principal,
			BasePriority: executor.priority, PriorityCeiling: executor.priority, MaxConcurrent: 1,
			Budget: ExecutorResource{Cores: 1},
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.HeartbeatExecutor(ctx, identity, executor.name, ExecutorResource{Cores: 1}, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"active", "work"} {
		if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: nodeID, Status: "pending", RequestedCores: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET
 claimed_by = 'holder', claim_executor = 'high', claim_cores = 1,
 claim_slot = 0, lease_expires_at = ?
 WHERE run_id = 'run' AND node_id = 'active'`, time.Now().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}

	release, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.ExecContext(ctx, `UPDATE nodes SET status = 'done'
 WHERE run_id = 'run' AND node_id = 'active'`); err != nil {
		_ = release.Rollback()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.MarkNodeReady(ctx, "run", "work") }()
	select {
	case err := <-done:
		_ = release.Rollback()
		t.Fatalf("MarkNodeReady passed an uncommitted slot release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := release.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MarkNodeReady did not resume after slot release committed")
	}
	node, err := st.GetNode(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if node.OfferPriorityTarget != 80 {
		t.Fatalf("offer priority target = %d, want newly available priority 80", node.OfferPriorityTarget)
	}
}

func TestExecutorEnrollmentLimitIsAtomic(t *testing.T) {
	st := newExecutorLockTestStore(t)
	ctx := context.Background()
	seedExecutorRegistrations(t, st, MaxEnrolledExecutors-1)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("boundary-%d", i)
			errs <- st.EnrollExecutor(ctx, "swr_"+name, Executor{
				Name: name, Kind: "agent", Location: "local", Principal: "principal-" + name,
				MaxConcurrent: 1,
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, limited := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrExecutorEnrollmentLimit):
			limited++
		default:
			t.Fatalf("concurrent enrollment error = %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent enrollment results: succeeded=%d limited=%d", succeeded, limited)
	}
	var count int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM executors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != MaxEnrolledExecutors {
		t.Fatalf("enrolled executors = %d, want %d", count, MaxEnrolledExecutors)
	}
	if err := st.EnrollExecutor(ctx, "swr_seed-0", Executor{
		Name: "seed-0", Kind: "agent", Location: "cloud", Principal: "principal-seed-0", MaxConcurrent: 2,
	}); err != nil {
		t.Fatalf("update at enrollment limit: %v", err)
	}
}

func TestExecutorRoundWorkIsBoundedAtEnrollmentLimit(t *testing.T) {
	st := newExecutorLockTestStore(t)
	ctx := context.Background()
	seedExecutorRegistrations(t, st, MaxEnrolledExecutors)
	if err := st.CreateRun(ctx, Run{ID: "bounded", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "bounded", NodeID: "work", Status: "pending", RequestedCores: 1}); err != nil {
		t.Fatal(err)
	}
	opened := time.Now().Add(-nodeClaimOfferWindow - time.Second)
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET ready_at = ?, offer_started_at = ?, offer_priority_target = 100
 WHERE run_id = 'bounded' AND node_id = 'work'`, opened.UnixNano(), opened.UnixNano()); err != nil {
		t.Fatal(err)
	}
	summary, err := st.SchedulingSummary(ctx, "bounded", "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxEnrolledExecutors {
		name := fmt.Sprintf("seed-%d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_claim_offers
 (claim_token_prefix, claim_principal, holder_id, run_id, node_id, executor_name,
  membership_id, worker_id, executor_kind, reservation_id, resource_digest, slot,
  base_priority, effective_priority, offered_at, last_seen_at, lease_ns)
 VALUES (?, ?, ?, 'bounded', 'work', ?, '', ?, 'agent', ?, ?, 0, 50, 50, ?, ?, ?)`,
			"swr_"+name, "principal-"+name, "holder-"+name, name, name,
			"reservation-"+name, summary.ResourceDigest, opened.UnixNano(), time.Now().UnixNano(), int64(time.Minute)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := st.FinalizeExecutorClaimRound(deadlineCtx, "bounded", "work"); err != nil {
		t.Fatalf("finalize %d offers: %v", MaxEnrolledExecutors, err)
	}
	node, err := st.GetNode(ctx, "bounded", "work")
	if err != nil {
		t.Fatal(err)
	}
	if node.ClaimedBy == "" {
		t.Fatal("bounded deadline round did not award an offer")
	}
}

func TestExecutorRoundRejectsCorruptCardinalityAboveLimit(t *testing.T) {
	st := newExecutorLockTestStore(t)
	ctx := context.Background()
	seedExecutorRegistrations(t, st, MaxEnrolledExecutors+1)
	if err := st.CreateRun(ctx, Run{ID: "overflow", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "overflow", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "overflow", "work"); !errors.Is(err, ErrExecutorEnrollmentLimit) {
		t.Fatalf("MarkNodeReady over executor limit = %v", err)
	}
	var opened any
	if err := st.DB().QueryRowContext(ctx, `SELECT offer_started_at FROM nodes WHERE run_id = 'overflow' AND node_id = 'work'`).Scan(&opened); err != nil {
		t.Fatal(err)
	}
	if opened != nil {
		t.Fatalf("overflow round opened at %v", opened)
	}
}

func TestExecutorAwardRejectsCorruptOfferCardinalityAboveLimit(t *testing.T) {
	st := newExecutorLockTestStore(t)
	ctx := context.Background()
	seedExecutorRegistrations(t, st, 1)
	if err := st.CreateRun(ctx, Run{ID: "offer-overflow", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "offer-overflow", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	opened := time.Now().Add(-nodeClaimOfferWindow - time.Second)
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET ready_at = ?, offer_started_at = ?
 WHERE run_id = 'offer-overflow' AND node_id = 'work'`, opened.UnixNano(), opened.UnixNano()); err != nil {
		t.Fatal(err)
	}
	summary, err := st.SchedulingSummary(ctx, "offer-overflow", "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxEnrolledExecutors + 1 {
		name := fmt.Sprintf("orphan-%d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_claim_offers
 (claim_token_prefix, claim_principal, holder_id, run_id, node_id, executor_name,
  membership_id, worker_id, executor_kind, reservation_id, resource_digest, slot,
  base_priority, effective_priority, offered_at, last_seen_at, lease_ns)
 VALUES (?, ?, ?, 'offer-overflow', 'work', ?, '', ?, 'agent', ?, ?, 0, 50, 50, ?, ?, ?)`,
			"swr_"+name, "principal-"+name, "holder-"+name, name, name,
			"reservation-"+name, summary.ResourceDigest, opened.UnixNano(), time.Now().UnixNano(), int64(time.Minute)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinalizeExecutorClaimRound(ctx, "offer-overflow", "work"); !errors.Is(err, errExecutorOfferLimit) {
		t.Fatalf("finalize over offer limit = %v", err)
	}
}

func newExecutorLockTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedExecutorRegistrations(t *testing.T, st *Store, count int) {
	t.Helper()
	ctx := context.Background()
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	for i := range count {
		name := fmt.Sprintf("seed-%d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO executors
 (executor_id, name, token_prefix, kind, location, capabilities_json,
  base_priority, priority_ceiling, max_concurrent, budget_cores, budget_memory_bytes,
  principal, last_seen, headroom_reported, headroom_cores, headroom_memory_bytes, queue_depth)
 VALUES (?, ?, ?, 'agent', 'local', '[]', 50, 50, 1, 8, 17179869184, ?, ?, 1, 8, 17179869184, 0)`,
			"swe_seed-"+fmt.Sprint(i), name, "swr_"+name, "principal-"+name, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
