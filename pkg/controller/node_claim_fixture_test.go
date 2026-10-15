package controller_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedRunNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	if err := st.CreateRun(t.Context(), store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
}
