package store

import (
	"context"
	"testing"
	"time"
)

// seedConcurrencyRun creates a run row so a concurrency row can point at
// a run with a known status.
func seedConcurrencyRun(t *testing.T, s *Store, id, status string) {
	t.Helper()
	if err := s.CreateRun(context.Background(), Run{
		ID: id, Pipeline: "demo", Status: status, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun %s: %v", id, err)
	}
}

func seedHolder(t *testing.T, s *Store, key, runID string) {
	t.Helper()
	_, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO concurrency_holders (key, holder_id, run_id, claimed_at, lease_expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		key, runID+":n", runID, time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano())
	if err != nil {
		t.Fatalf("seed holder %s: %v", key, err)
	}
}

func seedWaiter(t *testing.T, s *Store, key, runID string) {
	t.Helper()
	_, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO concurrency_waiters (key, run_id, arrived_at, policy)
		 VALUES (?, ?, ?, 'queue')`,
		key, runID, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("seed waiter %s: %v", key, err)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestPurgeDeadLocalConcurrency_RemovesLocalDeadKeepsGlobalAndLive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seedConcurrencyRun(t, s, "run-live", "running")
	seedConcurrencyRun(t, s, "run-dead", "failed")

	seedHolder(t, s, "r:run-dead:build", "run-dead")
	seedHolder(t, s, "b:host-1:deploy", "run-dead")
	seedWaiter(t, s, "r:run-dead:test", "run-dead")

	seedHolder(t, s, "r:run-live:build", "run-live")

	seedHolder(t, s, "g:release-lock", "run-dead")
	seedWaiter(t, s, "g:release-lock", "run-dead")

	holders, waiters, err := s.PurgeDeadLocalConcurrency(ctx)
	if err != nil {
		t.Fatalf("PurgeDeadLocalConcurrency: %v", err)
	}
	if holders != 2 || waiters != 1 {
		t.Fatalf("removed (%d holders, %d waiters), want (2, 1)", holders, waiters)
	}
	if got := countRows(t, s, "concurrency_holders"); got != 2 {
		t.Fatalf("remaining holders = %d, want 2 (live local + global)", got)
	}
	if got := countRows(t, s, "concurrency_waiters"); got != 1 {
		t.Fatalf("remaining waiters = %d, want 1 (global)", got)
	}

	h2, w2, err := s.PurgeDeadLocalConcurrency(ctx)
	if err != nil {
		t.Fatalf("second PurgeDeadLocalConcurrency: %v", err)
	}
	if h2 != 0 || w2 != 0 {
		t.Fatalf("second pass removed (%d, %d), want (0, 0)", h2, w2)
	}
}

func TestCountDeadLocalConcurrency_MatchesPurgeWithoutRemoving(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedConcurrencyRun(t, s, "run-dead", "cancelled")
	seedHolder(t, s, "r:run-dead:build", "run-dead")
	seedWaiter(t, s, "b:host-1:deploy", "run-dead")

	h, w, err := s.CountDeadLocalConcurrency(ctx)
	if err != nil {
		t.Fatalf("CountDeadLocalConcurrency: %v", err)
	}
	if h != 1 || w != 1 {
		t.Fatalf("count = (%d, %d), want (1, 1)", h, w)
	}
	if got := countRows(t, s, "concurrency_holders"); got != 1 {
		t.Fatalf("count must not delete: holders = %d, want 1", got)
	}
}
