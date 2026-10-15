package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func ackNodeAttempt(t *testing.T, s *store.Store, n *store.Node, identity store.ClaimIdentity, ordinal int) {
	t.Helper()
	team, err := s.AsOperator().RunTeam(context.Background(), n.RunID)
	if err != nil {
		t.Fatal(err)
	}
	tn, err := s.ForTeam(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.AcknowledgeNodeExecutionStart(context.Background(), n.RunID, n.NodeID, identity, store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID:   n.ReservationID,
		ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: ordinal,
	}); err != nil {
		t.Fatal(err)
	}
}

func claimNode(t *testing.T, s *store.Store, runID string, identity store.ClaimIdentity, holder string) *store.Node {
	t.Helper()
	n, err := s.ClaimNextReadyNode(context.Background(), identity, holder, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.RunID != runID {
		t.Fatalf("claimed run %q, want %q", n.RunID, runID)
	}
	return n
}

func claimRetryNode(t *testing.T, s *store.Store, runID string, identity store.ClaimIdentity, holder string) *store.Node {
	t.Helper()
	n := claimNode(t, s, runID, identity, holder)
	if _, err := s.DB().ExecContext(context.Background(), storetest.Rebind(s, `UPDATE nodes
   SET executor_location = 'local'
 WHERE run_id = ? AND node_id = ?`), n.RunID, n.NodeID); err != nil {
		t.Fatal(err)
	}
	n.ExecutorLocation = "local"
	return n
}

func createRetryRunAndReadyNode(t *testing.T, s *store.Store, runID string, retries int) {
	t.Helper()
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": runID,
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": retries}}},
	})
	if err := s.CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(context.Background(), store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(context.Background(), runID, "build"); err != nil {
		t.Fatal(err)
	}
}

func forceExpireNodeClaim(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(), storetest.Rebind(s, `UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), runID, nodeID); err != nil {
		t.Fatal(err)
	}
}

func seedSubmittedRun(t *testing.T, s *store.Store, id, pipeline string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: pipeline, CreatedAt: now, TriggerSource: "runs-submit",
	}); err != nil {
		t.Fatalf("seed trigger %s: %v", id, err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: id, Pipeline: pipeline, Status: "pending",
		TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}

func expireTriggerClaim(t *testing.T, s *store.Store, id string) {
	t.Helper()
	res, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE triggers SET lease_expires_at = ? WHERE id = ? AND status = 'claimed' AND lease_expires_at IS NOT NULL`),
		time.Now().Add(-time.Second).UnixNano(), id,
	)
	if err != nil {
		t.Fatalf("expire trigger claim: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count expired trigger claims: %v", err)
	}
	if changed != 1 {
		t.Fatalf("expired trigger claims = %d, want 1", changed)
	}
}
