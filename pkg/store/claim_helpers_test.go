package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func createRetryRunAndReadyNode(t *testing.T, s *store.Store, runID string, retries int) {
	t.Helper()
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": runID,
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": retries}}},
	})
	if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(s, context.Background(), store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(s, context.Background(), runID, "build"); err != nil {
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
	if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: pipeline, CreatedAt: now, TriggerSource: "runs-submit",
	}); err != nil {
		t.Fatalf("seed trigger %s: %v", id, err)
	}
	if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateRun(ctx, store.Run{
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
