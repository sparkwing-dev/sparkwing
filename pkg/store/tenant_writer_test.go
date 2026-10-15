package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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

	_, tok, err := tn.CreateTokenWith(ctx, "ci", store.TokenKindService,
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
}

// A node is told no team by its caller; it belongs to a run, and the run
// carries the right one.
func TestCreatedNodeCarriesItsRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	tn := tenantFor(t, st, "acme")
	seedTenantRun(t, tn, "run-acme", "build")

	if err := teststore.SeedNode(st, ctx, store.Node{
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
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateTrigger(ctx, store.Trigger{ID: "trg-local", Pipeline: "build", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "trg-local"); got != store.DefaultTeam {
		t.Errorf("unscoped trigger carries team %q, want %q", got, store.DefaultTeam)
	}
}

// safety: an empty table would pass a check of every row's team, so the
// count has to be nonzero before the team says anything.
func requireRunRowsInTeam(t *testing.T, st *store.Store, table, runID string, team store.Team) {
	t.Helper()
	var total, wrong int
	if err := st.DB().QueryRowContext(context.Background(), storetest.Rebind(st,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN team = ? THEN 0 ELSE 1 END), 0) FROM `+table+` WHERE run_id = ?`),
		string(team), runID).Scan(&total, &wrong); err != nil {
		t.Fatalf("read %s rows: %v", table, err)
	}
	if total == 0 {
		t.Errorf("%s holds no row for %s, so its team was never proven", table, runID)
	}
	if wrong != 0 {
		t.Errorf("%d of %d %s rows for %s carry a team other than %s", wrong, total, table, runID, team)
	}
}
