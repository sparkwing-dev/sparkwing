package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: reads the row back through the same store the claim reads, because
// a test that writes the team itself proves the predicate's text and nothing
// about whether any writer fills the column.
func nodeTeam(t *testing.T, st *store.Store, runID, nodeID string) string {
	t.Helper()
	var team string
	row := st.DB().QueryRow(storetest.Rebind(st,
		`SELECT team FROM nodes WHERE run_id = ? AND node_id = ?`), runID, nodeID)
	if err := row.Scan(&team); err != nil {
		t.Fatalf("read node team: %v", err)
	}
	return team
}

func seedReadyNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("CreateNode(%s/%s): %v", runID, nodeID, err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady(%s/%s): %v", runID, nodeID, err)
	}
}

func mintClaimant(t *testing.T, st *store.Store, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := st.CreateToken(principal, store.TokenKindRunner,
		[]string{"nodes.claim", "runs.state"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken(%s): %v", principal, err)
	}
	return store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
}

func mintTeamClaimant(t *testing.T, tn *store.Tenant, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := tn.CreateToken(context.Background(), principal, store.TokenKindRunner,
		[]string{"triggers.claim", "nodes.claim", "runs.state"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken(%s): %v", principal, err)
	}
	return store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
}

func requireTriggerStatus(t *testing.T, st *store.Store, id, want string) {
	t.Helper()
	team, err := st.AsOperator().TriggerTeam(context.Background(), id)
	if err != nil {
		t.Fatalf("TriggerTeam(%s): %v", id, err)
	}
	tn, err := st.ForTeam(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tn.GetTrigger(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTrigger(%s): %v", id, err)
	}
	if got.Status != want {
		t.Fatalf("trigger %s is %q, want %q", id, got.Status, want)
	}
}
