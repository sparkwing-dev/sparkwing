package store_test

import (
	"context"
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
