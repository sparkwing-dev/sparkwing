package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// A hold takes the team's ledger lock a metered claim takes, so a claim that
// read the team as not held commits before the hold does and none can commit
// after it. The test holds the team's lock and shows the hold waits for it.
func TestAHoldWaitsForTheLedgerLock(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx := context.Background()
	tenant := teststore.ForTeam(t, s, store.DefaultTeam)
	holder, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, "sparkwing/ledger/"+string(store.DefaultTeam)); err != nil {
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
