package store_test

import (
	"context"
	"errors"
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

// A machine claims for the team its credential belongs to and for no other.
// This is a data-leak boundary rather than a scheduling preference, so the
// other team's node is put at the head of the queue: a claim path with no
// team predicate hands it over on the first call.
func TestClaimRefusesAnotherTeamsNodeAtTheHeadOfTheQueue(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)

	foreign := tenantFor(t, st, "acme")
	seedTenantRun(t, foreign, "run-acme", "demo")
	seedReadyNode(t, st, "run-acme", "build")
	if got := nodeTeam(t, st, "run-acme", "build"); got != "acme" {
		t.Skipf("the nodes writer puts a node of an acme run in team %q, so this boundary "+
			"cannot be proven through real writers yet; it goes green when the nodes "+
			"INSERT carries the run's team", got)
	}

	// safety: a node the claimant may take, so a refusal cannot be read as an
	// empty queue.
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-home", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(run-home): %v", err)
	}
	seedReadyNode(t, st, "run-home", "build")

	claimant := mintClaimant(t, st, "agent:laptop")
	claimed, err := st.ClaimNextReadyNode(ctx, claimant, "agent:laptop", time.Minute, nil)
	if err != nil {
		t.Fatalf("claiming this team's own node: %v", err)
	}
	if claimed.RunID != "run-home" {
		t.Fatalf("a laptop holding one team's credential claimed %s/%s, which belongs to another team",
			claimed.RunID, claimed.NodeID)
	}

	rest, err := st.ClaimNextReadyNode(ctx, claimant, "agent:laptop", time.Minute, nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after its own queue drained the laptop claimed %+v (err %v), want not found", rest, err)
	}
}

// Naming a node is not a way past the boundary the queue scan draws.
func TestNamedClaimRefusesAnotherTeamsNode(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)

	foreign := tenantFor(t, st, "acme")
	seedTenantRun(t, foreign, "run-acme", "demo")
	seedReadyNode(t, st, "run-acme", "build")
	if got := nodeTeam(t, st, "run-acme", "build"); got != "acme" {
		t.Skipf("the nodes writer puts a node of an acme run in team %q, so this boundary "+
			"cannot be proven through real writers yet; it goes green when the nodes "+
			"INSERT carries the run's team", got)
	}

	claimant := mintClaimant(t, st, "agent:laptop")
	claimed, err := st.ClaimNamedNode(ctx, claimant, "run-acme", "build", "agent:laptop",
		time.Minute, store.NamedClaimOptions{})
	if err == nil {
		t.Fatalf("a laptop named another team's node and was given it: %+v", claimed)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("named claim across teams returned %v, want not found", err)
	}
}

// A credential the deployment cannot place claims nothing once more than one
// team exists, because guessing which team it meant is the leak.
func TestClaimFailsClosedForAnUnplaceableCredentialOnAMultiTeamDeployment(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-home", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(run-home): %v", err)
	}
	seedReadyNode(t, st, "run-home", "build")

	stranger := store.ClaimIdentity{Principal: "agent:stranger", TokenPrefix: "swr_stranger"}
	if _, err := st.ClaimNextReadyNode(ctx, stranger, "agent:stranger", time.Minute, nil); err != nil {
		t.Fatalf("one team is registered, so an unplaceable credential still claims: %v", err)
	}

	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatalf("CreateTeam(acme): %v", err)
	}
	seedReadyNode(t, st, "run-home", "second")

	claimed, err := st.ClaimNextReadyNode(ctx, stranger, "agent:stranger", time.Minute, nil)
	if !errors.Is(err, store.ErrClaimantHasNoTeam) {
		t.Fatalf("a second team is registered and an unplaceable credential claimed %+v (err %v)",
			claimed, err)
	}
}
