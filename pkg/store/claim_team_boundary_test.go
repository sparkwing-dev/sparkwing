package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
		t.Fatalf("the nodes writer put a node of an acme run in team %q, so the "+
			"boundary below would be proven against the wrong team", got)
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
		t.Fatalf("the nodes writer put a node of an acme run in team %q, so the "+
			"boundary below would be proven against the wrong team", got)
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
	got, err := st.GetTrigger(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTrigger(%s): %v", id, err)
	}
	if got.Status != want {
		t.Fatalf("trigger %s is %q, want %q", id, got.Status, want)
	}
}

// A claimed trigger is the run a runner executes and the secrets it reads,
// so a laptop holding one team's runner token must not reach another team's
// trigger by the queue or by naming its id. The other team's trigger is the
// oldest, so a queue claim with no team predicate takes it first.
func TestTriggerClaimRefusesAnotherTeamsTrigger(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	bravo := tenantFor(t, st, "bravo")

	if err := bravo.CreateTrigger(ctx, store.Trigger{
		ID: "trg-bravo", Pipeline: "deploy", CreatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateTrigger(bravo): %v", err)
	}
	if err := alpha.CreateTrigger(ctx, store.Trigger{
		ID: "trg-alpha", Pipeline: "deploy", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger(alpha): %v", err)
	}
	laptop := mintTeamClaimant(t, alpha, "agent:alpha-laptop")

	claimed, err := st.ClaimNextTriggerFor(ctx, laptop, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("claiming this team's own trigger: %v", err)
	}
	if claimed.ID != "trg-alpha" {
		t.Fatalf("an alpha runner token claimed trigger %s, which belongs to bravo", claimed.ID)
	}
	if rest, err := st.ClaimNextTriggerFor(ctx, laptop, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after its own queue drained the alpha runner claimed %+v (err %v), want not found", rest, err)
	}

	named, err := st.ClaimSpecificTriggerFor(ctx, "trg-bravo", laptop, time.Minute)
	if err == nil {
		t.Fatalf("an alpha runner named bravo's trigger and was given it: %+v", named)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("named trigger claim across teams returned %v, want not found", err)
	}
	requireTriggerStatus(t, st, "trg-bravo", "pending")

	// safety: bravo's own runner still takes it, so the refusal above cannot
	// be read as the trigger being unclaimable.
	bravoRunner := mintTeamClaimant(t, bravo, "agent:bravo-laptop")
	if got, err := st.ClaimSpecificTriggerFor(ctx, "trg-bravo", bravoRunner, time.Minute); err != nil || got.ID != "trg-bravo" {
		t.Fatalf("bravo's runner claiming its own trigger = %+v, %v", got, err)
	}
}

// Metering decides who pays, not whose work a claimant sees. An admin can
// flip any token to metered, and a cloud runner hands its token to the
// pipeline code it executes, so a metered credential that read every team's
// queue would give each team's code every other team's nodes and secrets.
func TestMeteredTokenClaimsOnlyItsOwnTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	seedTenantRun(t, acme, "run-acme", "demo")
	seedReadyNode(t, st, "run-acme", "build")

	// safety: both teams can pay, so a claim across the boundary is refused
	// for being across it and never for an empty balance.
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_home", "admin"); err != nil {
		t.Fatalf("GrantCredits: %v", err)
	}
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_acme", "admin"); err != nil {
		t.Fatalf("GrantCredits(acme): %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-home", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(run-home): %v", err)
	}
	seedReadyNode(t, st, "run-home", "build")

	pool := meteredClaimant(t, st, "agent:cloud")
	claimed, err := st.ClaimNextReadyNode(ctx, pool, "pod-1", time.Minute, nil)
	if err != nil {
		t.Fatalf("a metered token claiming its own team's node: %v", err)
	}
	if claimed.RunID != "run-home" {
		t.Fatalf("a metered token claimed %s/%s, which belongs to another team", claimed.RunID, claimed.NodeID)
	}
	if rest, err := st.ClaimNextReadyNode(ctx, pool, "pod-2", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after its own queue drained a metered token claimed %+v (err %v), want not found", rest, err)
	}
	if named, err := st.ClaimNamedNode(ctx, pool, "run-acme", "build", "pod-3",
		time.Minute, store.NamedClaimOptions{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a metered token named another team's node and got %+v (err %v), want not found", named, err)
	}

	// safety: flipping the flag afterwards is the admin endpoint's move, and
	// it must not widen what an existing team credential reaches either.
	laptop := mintTeamClaimant(t, acme, "agent:acme-laptop")
	if err := st.SetTokenMetered(ctx, laptop.TokenPrefix, true); err != nil {
		t.Fatalf("SetTokenMetered: %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-home-2", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(run-home-2): %v", err)
	}
	seedReadyNode(t, st, "run-home-2", "build")
	got, err := st.ClaimNextReadyNode(ctx, laptop, "acme-laptop", time.Minute, nil)
	if err != nil {
		t.Fatalf("an acme token flipped to metered claiming its own node: %v", err)
	}
	if got.RunID != "run-acme" {
		t.Fatalf("an acme token flipped to metered claimed %s/%s, which belongs to another team", got.RunID, got.NodeID)
	}
	if rest, err := st.ClaimNextReadyNode(ctx, laptop, "acme-laptop", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after acme's queue drained its flipped token claimed %+v (err %v), want not found", rest, err)
	}
}

// Two teams' runners drain their queues at once. Every trigger and node goes
// to a runner of its own team exactly once, which is the property a skip-locked
// scan has to keep when the team predicate and the lock meet under contention.
func TestConcurrentClaimsNeverCrossTeams(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	const perTeam = 6
	teams := map[store.Team]*store.Tenant{
		"alpha": tenantFor(t, st, "alpha"),
		"bravo": tenantFor(t, st, "bravo"),
	}
	owner := map[string]store.Team{}
	for team, tn := range teams {
		for i := range perTeam {
			id := fmt.Sprintf("%s-%d", team, i)
			if err := tn.CreateTrigger(ctx, store.Trigger{ID: "trg-" + id, Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
				t.Fatalf("CreateTrigger(%s): %v", id, err)
			}
			seedTenantRun(t, tn, "run-"+id, "demo")
			seedReadyNode(t, st, "run-"+id, "build")
			owner["trg-"+id] = team
			owner["run-"+id] = team
		}
	}

	type result struct {
		team store.Team
		id   string
	}
	results := make(chan result, 4*perTeam*len(teams))
	errs := make(chan error, 8*len(teams))
	var wg sync.WaitGroup
	for team, tn := range teams {
		for w := range 4 {
			runner := mintTeamClaimant(t, tn, fmt.Sprintf("agent:%s-%d", team, w))
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					trig, err := st.ClaimNextTriggerFor(ctx, runner, time.Minute, nil, nil)
					if errors.Is(err, store.ErrNotFound) {
						break
					}
					if err != nil {
						errs <- err
						return
					}
					results <- result{team, trig.ID}
				}
				for {
					n, err := st.ClaimNextReadyNode(ctx, runner, runner.Principal, time.Minute, nil)
					if errors.Is(err, store.ErrNotFound) {
						return
					}
					if err != nil {
						errs <- err
						return
					}
					results <- result{team, n.RunID}
				}
			}()
		}
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("claim under contention: %v", err)
	}
	seen := map[string]bool{}
	for r := range results {
		if seen[r.id] {
			t.Errorf("%s was claimed twice", r.id)
		}
		seen[r.id] = true
		if owner[r.id] != r.team {
			t.Errorf("a %s runner claimed %s, which belongs to %s", r.team, r.id, owner[r.id])
		}
	}
	if len(seen) != len(owner) {
		t.Errorf("claimed %d of %d triggers and nodes", len(seen), len(owner))
	}
}
