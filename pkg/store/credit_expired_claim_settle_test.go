package store_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func usageSeconds(t *testing.T, s *store.Store, runID, nodeID string) int64 {
	t.Helper()
	var seconds int64
	if err := s.DB().QueryRow(fmt.Sprintf(
		`SELECT COALESCE(SUM(seconds), 0) FROM credit_charges
		  WHERE run_id = '%s' AND node_id = '%s' AND kind = '%s'`,
		runID, nodeID, store.CreditChargeUsage)).Scan(&seconds); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return seconds
}

// On Postgres the reaper skips a claim another transaction holds, so it must
// clear only the claims it selected and settled: a skipped claim keeps its
// claim and charge window for the next pass to settle, even when the holder
// lets go while the reaper is still working.
func TestReaperLeavesAClaimItSkippedForTheNextPass(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx := context.Background()
	claimant, _ := fundedMeteredNode(t, s, "run-a")
	readyNode(t, s, "run-b", "build")
	for _, runID := range []string{"run-a", "run-b"} {
		n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-"+runID, time.Minute, nil)
		if err != nil || n == nil {
			t.Fatalf("claim %s: %v", runID, err)
		}
		rewindChargeWindow(t, s, n.RunID, n.NodeID, time.Now().Add(-40*time.Second))
		expireNodeLease(t, s, n.RunID, n.NodeID, time.Now().Add(-30*time.Second))
	}
	holder, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx,
		`SELECT 1 FROM nodes WHERE run_id = 'run-b' AND node_id = 'build' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := teststore.ReapUnits(ctx, s)
		done <- err
	}()
	// safety: the holder keeps the row until the pass ends, so the pass must skip it rather than block.
	// Only the holder's own blockees count: other packages' tests share the database, and a lock wait
	// of theirs can release the row early and let the pass settle it legitimately.
	var reapErr error
	finished := false
	for !finished {
		select {
		case reapErr = <-done:
			finished = true
			continue
		default:
		}
		var blocked int
		if err := s.DB().QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM pg_stat_activity
			WHERE %d = ANY(pg_blocking_pids(pid))`, holderPID)).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		runtime.Gosched()
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !finished {
		reapErr = <-done
	}
	if reapErr != nil {
		t.Fatalf("reap: %v", reapErr)
	}
	claimed := func(runID string) bool {
		var n int
		if err := s.DB().QueryRowContext(ctx, fmt.Sprintf(
			`SELECT COUNT(*) FROM nodes WHERE run_id = '%s' AND claimed_by IS NOT NULL AND credit_charged_through != 0`,
			runID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if claimed("run-a") {
		t.Error("the claim the reaper selected is still held")
	}
	if !claimed("run-b") {
		t.Fatal("the claim the reaper skipped was cleared without its tail settled")
	}
	if got := usageSeconds(t, s, "run-b", "build"); got != 0 {
		t.Fatalf("the skipped claim was billed %d seconds by a pass that skipped it", got)
	}
	if _, err := teststore.ReapUnits(ctx, s); err != nil {
		t.Fatal(err)
	}
	if claimed("run-b") || usageSeconds(t, s, "run-b", "build") == 0 {
		t.Fatal("the next pass did not settle and clear the skipped claim")
	}
}

// A hold takes the ledger lock a metered claim takes, so a claim that read
// the team as not frozen commits before the hold does and none can commit
// after it. The test holds the ledger lock and shows the hold waits for it.
func TestAHoldWaitsForTheLedgerLock(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx := context.Background()
	tenant := teststore.ForTeam(t, s, store.DefaultTeam)
	holder, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('sparkwing/credit-ledger'))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := tenant.TestOnlyHoldForDispute(ctx, "dp_lock", "", "opened", time.Now())
		done <- err
	}()
	waited := false
	for !waited {
		select {
		case err := <-done:
			_ = holder.Rollback()
			t.Fatalf("the hold finished (%v) while another transaction held the ledger lock", err)
		default:
		}
		var waiting int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		waited = waiting > 0
		runtime.Gosched()
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("hold: %v", err)
	}
}
