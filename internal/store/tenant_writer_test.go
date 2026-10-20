package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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

	if err := tn.TestOnlySeedTrigger(ctx, store.Trigger{
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
