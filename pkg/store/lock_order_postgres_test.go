package store_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// An attempt report locks its run and then its node. The expired-claim reaper
// must take the same order: holding the node while it waits for the run would
// deadlock a report whose claim lapsed as it arrived.
func TestLockOrder_PostgresReaperTakesTheRunBeforeTheNode(t *testing.T) {
	f := newDispatchRunOn(t, storetest.NewPostgres(t).Open(t), "run-lock-order")
	ctx := context.Background()
	f.mustAccept(t, planOf("a"))
	f.claim(t, "a", store.ClaimTokenWork)
	db := f.s.DB()
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET lease_expires_at = $1 WHERE run_id = $2 AND node_id = 'a'`,
		time.Now().Add(-time.Second).UnixNano(), f.run); err != nil {
		t.Fatal(err)
	}
	report, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.Rollback() }()
	if _, err := report.ExecContext(ctx, `SELECT id FROM runs WHERE id = $1 FOR NO KEY UPDATE`, f.run); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := report.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Maintenance.FailExpiredNodeClaims(f.s, ctx)
		done <- err
	}()
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("the reaper finished while the run row was held: %v", err)
		default:
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if _, err := report.ExecContext(ctx, `SELECT 1 FROM nodes WHERE run_id = $1 AND node_id = 'a' FOR UPDATE NOWAIT`, f.run); err != nil {
		t.Fatalf("the reaper holds the node while it waits for the run: %v", err)
	}
	if err := report.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("reaper: %v", err)
	}
	f.wantOutcome(t, "a", "failed")
}

// A pass locks its runs through one bind parameter each, so it takes a bounded
// batch; the runs past it recover on the next pass rather than failing all.
func TestExpiredClaimReaper_RecoversABoundedBatchPerPass(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: seeds more expired runs than one pass takes")
	}
	s := storetest.Open(t)
	ctx := context.Background()
	total := store.MaxExpiredClaimRunsPerPass + 3
	expired := time.Now().Add(-time.Minute).UnixNano()
	for i := range total {
		run := fmt.Sprintf("run-sweep-%04d", i)
		if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
			`INSERT INTO runs (id, pipeline, status, started_at) VALUES (?, 'demo', 'pending', ?)`), run, expired); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
			`INSERT INTO nodes (run_id, node_id, status, claimed_by, lease_expires_at) VALUES (?, 'n', 'running', 'holder', ?)`),
			run, expired); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Maintenance.FailExpiredNodeClaims(s, ctx)
	if err != nil || len(first) != store.MaxExpiredClaimRunsPerPass {
		t.Fatalf("first pass recovered %d (%v), want %d", len(first), err, store.MaxExpiredClaimRunsPerPass)
	}
	second, err := store.Maintenance.FailExpiredNodeClaims(s, ctx)
	if err != nil || len(second) != total-store.MaxExpiredClaimRunsPerPass {
		t.Fatalf("second pass recovered %d (%v), want the %d left", len(second), err, total-store.MaxExpiredClaimRunsPerPass)
	}
	if third, err := store.Maintenance.FailExpiredNodeClaims(s, ctx); err != nil || len(third) != 0 {
		t.Fatalf("third pass recovered %d (%v)", len(third), err)
	}
}
