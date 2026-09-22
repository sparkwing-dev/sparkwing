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

// safety: reads the stored column itself rather than a struct field, so a
// writer that drops the team cannot be hidden by a reader that supplies
// one.
func storedTeam(t *testing.T, st *store.Store, query string, args ...any) store.Team {
	t.Helper()
	var team string
	if err := st.DB().QueryRowContext(
		context.Background(), storetest.Rebind(st, query), args...,
	).Scan(&team); err != nil {
		t.Fatalf("read the stored team (%s): %v", query, err)
	}
	return store.Team(team)
}

// Every scope test before this one fabricated the team column with a
// hand-rolled INSERT, which pins a predicate's text and never a writer.
// These three go through the public mint, node create and trigger create
// and then read the column the guards will filter on.
func TestMintedTokenCarriesItsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")

	raw, tok, err := tn.CreateTokenWith(ctx, "ci", store.TokenKindService,
		[]string{"run:submit"}, time.Hour, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatalf("CreateTokenWith: %v", err)
	}
	if tok.Team != "acme" {
		t.Errorf("minted token reports team %q, want acme", tok.Team)
	}
	if got := storedTeam(t, st, `SELECT team FROM tokens WHERE prefix = ?`, tok.Prefix); got != "acme" {
		t.Errorf("stored token row carries team %q, want acme", got)
	}
	// safety: a bearer lookup that cannot name the team resolves no tenant
	// for the request, so the authentication path is where this has to hold.
	looked, err := st.LookupToken(raw, time.Now())
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if looked.Team != "acme" {
		t.Errorf("LookupToken reports team %q, want acme", looked.Team)
	}
}

// A rotation re-mints from the row it replaces, so the replacement has to
// inherit the team rather than the default.
func TestRotatedTokenKeepsItsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")

	_, tok, err := tn.CreateToken(ctx, "ci", store.TokenKindService, []string{"run:submit"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	_, rotated, _, err := st.RotateToken(tok.Prefix, time.Minute, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if rotated.Team != "acme" {
		t.Errorf("rotated token reports team %q, want acme", rotated.Team)
	}
	if got := storedTeam(t, st, `SELECT team FROM tokens WHERE prefix = ?`, rotated.Prefix); got != "acme" {
		t.Errorf("stored replacement carries team %q, want acme", got)
	}
}

// A node is told no team by its caller; it belongs to a run, and the run
// carries the right one.
func TestCreatedNodeCarriesItsRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")
	seedTenantRun(t, tn, "run-acme", "build")

	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-acme",
		NodeID: "compile",
		Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got := storedTeam(t, st,
		`SELECT team FROM nodes WHERE run_id = ? AND node_id = ?`, "run-acme", "compile")
	if got != "acme" {
		t.Errorf("stored node row carries team %q, want acme", got)
	}
}

func TestCreatedTriggerCarriesItsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")

	if err := tn.CreateTrigger(ctx, store.Trigger{
		ID:        "trg-acme",
		Pipeline:  "build",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "trg-acme"); got != "acme" {
		t.Errorf("stored trigger row carries team %q, want acme", got)
	}
}

// CreateTriggerWithRun writes both rows in one transaction, so both have
// to land in the same team.
func TestCreatedTriggerWithRunPutsBothRowsInOneTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")

	if err := tn.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "trg-pair", Pipeline: "build", CreatedAt: time.Now()},
		store.Run{ID: "trg-pair", Pipeline: "build", Status: "pending", StartedAt: time.Now()},
	); err != nil {
		t.Fatalf("CreateTriggerWithRun: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "trg-pair"); got != "acme" {
		t.Errorf("stored trigger row carries team %q, want acme", got)
	}
	if got := storedTeam(t, st, `SELECT team FROM runs WHERE id = ?`, "trg-pair"); got != "acme" {
		t.Errorf("stored run row carries team %q, want acme", got)
	}
}

// The unscoped twins are what a single-tenant install still calls, and
// their rows have to stay where the migration put every pre-tenant row.
func TestUnscopedWritersStayInTheDefaultTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)

	_, tok, err := st.CreateToken("ci", store.TokenKindService, []string{"run:submit"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM tokens WHERE prefix = ?`, tok.Prefix); got != store.DefaultTeam {
		t.Errorf("unscoped mint carries team %q, want %q", got, store.DefaultTeam)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "trg-local", Pipeline: "build", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "trg-local"); got != store.DefaultTeam {
		t.Errorf("unscoped trigger carries team %q, want %q", got, store.DefaultTeam)
	}
}

// An agent-loss retry is written by the recovery sweep, which no caller
// hands a team, so it has to copy the lost run's team or the retry lands
// where the team that lost the agent cannot see or claim it.
func TestAgentLossRetryStaysInTheLostRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")

	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-lost",
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": 0}}},
	})
	if err := tn.CreateRun(ctx, store.Run{
		ID: "run-lost", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-lost", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-lost", "build"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	_, tok, err := tn.CreateToken(ctx, "agent:laptop", store.TokenKindRunner,
		[]string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	claimant := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	claimRetryNode(t, st, "run-lost", claimant, "agent:laptop:1")
	forceExpireNodeClaim(t, st, "run-lost", "build")

	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatalf("RecoverExpiredNodeClaims: %v", err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v, want one retry run", recovered)
	}
	retryID := recovered[0].RetryRunID
	if got := storedTeam(t, st, `SELECT team FROM runs WHERE id = ?`, retryID); got != "acme" {
		t.Errorf("stored retry run carries team %q, want acme", got)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, retryID); got != "acme" {
		t.Errorf("stored retry trigger carries team %q, want acme", got)
	}
}
