package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func seedRunWithNode(t *testing.T, st *store.Store, runID, nodeID, status string) {
	t.Helper()
	ctx := context.Background()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: status, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node %s/%s: %v", runID, nodeID, err)
	}
}

func appendEventAt(t *testing.T, st *store.Store, runID, nodeID, kind string, at time.Time) {
	t.Helper()
	if err := st.AppendEvent(context.Background(), runID, nodeID, kind, nil); err != nil {
		t.Fatalf("append event %s: %v", kind, err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE events SET ts = ? WHERE run_id = ? AND seq = (SELECT MAX(seq) FROM events WHERE run_id = ?)`), at.UnixNano(), runID, runID); err != nil {
		t.Fatalf("backdate event %s: %v", kind, err)
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
