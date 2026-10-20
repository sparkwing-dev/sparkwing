package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func createRunAndReadyNode(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := teststore.SeedNodeReady(s, ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}
