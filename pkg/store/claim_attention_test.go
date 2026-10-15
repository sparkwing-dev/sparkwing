package store_test

import (
	"context"

	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func ageReadyNode(t *testing.T, s *store.Store, runID string, age time.Duration) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(), storetest.Rebind(s,
		`UPDATE nodes SET ready_at = ?, placement_hold_from = ? WHERE run_id = ?`),
		time.Now().Add(-age).UnixNano(), time.Now().Add(-age).UnixNano(), runID); err != nil {
		t.Fatal(err)
	}
}

// The sweep writes one row per statement: while another transaction holds one
// node's lock, the reason for a different node is already committed rather
// than held behind it.
func TestSetNodeAttentionHoldsOneNodeLockAtATime(t *testing.T) {
	s := storetest.Open(t)
	if s.Dialect() != store.DialectPostgres {
		t.Skip("SQLite serializes every writer, so there is no row lock to order")
	}
	ctx := context.Background()
	seedMatchNode(t, s, "run-x", "", 1, "tool:terraform")
	seedMatchNode(t, s, "run-y", "", 1, "tool:terraform")
	holder, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.ExecContext(ctx, `SELECT 1 FROM nodes WHERE run_id = 'run-y' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.AsOperator().SetNodeAttention(ctx, []store.NodeAttention{
			{Team: store.DefaultTeam, RunID: "run-x", NodeID: "work", Reason: "x"},
			{Team: store.DefaultTeam, RunID: "run-y", NodeID: "work", Reason: "y"},
		})
	}()
	// safety: once the sweep waits on run-y's lock it has written run-x, so
	// run-x's reason must already be visible to another reader.
	blocked := 0
	for range 100000 {
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
 WHERE wait_event_type = 'Lock' AND query LIKE 'UPDATE nodes SET attention_reason%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
	}
	if blocked == 0 {
		t.Fatal("the sweep never waited on run-y's lock")
	}
	if run, err := s.GetRun(ctx, "run-x"); err != nil || run.NeedsAttention != "x" {
		t.Fatalf("run-x = %+v, %v; its reason is not committed while run-y's lock is held, so the sweep holds both", run, err)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if run, err := s.GetRun(ctx, "run-y"); err != nil || run.NeedsAttention != "y" {
		t.Fatalf("run-y = %+v, %v; want its reason once the lock is released", run, err)
	}
}

func seedMatchNode(t *testing.T, s *store.Store, runID, repoURL string, cores float64, needs ...string) {
	t.Helper()
	ctx := context.Background()
	seedRepoTrigger(t, s, runID, repoURL, time.Now())
	plan := `{"nodes":[{"id":"work","modifiers":{"res_cores":` + strconv.FormatFloat(cores, 'f', -1, 64) + `}}]}`
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `UPDATE runs SET plan_json = ? WHERE id = ?`), plan, runID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "work", Status: "pending", NeedsLabels: needs,
		RequestedCores: cores,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, runID, "work"); err != nil {
		t.Fatal(err)
	}
}
