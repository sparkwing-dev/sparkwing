package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func seedRunWithNode(t *testing.T, st *store.Store, runID, nodeID, status string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{RunSummary: runstate.RunSummary{ID: runID, Pipeline: "p", Status: status, StartedAt: time.Now().UTC()}}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
	if err := teststore.SeedNode(st, ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node %s/%s: %v", runID, nodeID, err)
	}
}

func countRows(t *testing.T, st *store.Store, query string) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(storetest.Rebind(st, query)).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

func storageTestTenant(t *testing.T, s *store.Store) *store.Tenant {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}
