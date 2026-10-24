package controller_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func seedRunNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	if err := teststore.SeedRun(st, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(st, t.Context(), store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
}
