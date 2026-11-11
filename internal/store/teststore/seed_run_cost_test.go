package teststore_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSeedRunLeavesExistingReportingRowsUntouched(t *testing.T) {
	s, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seed := store.Run{RunSummary: runstate.RunSummary{
		ID: "older", Pipeline: "build", Status: "success", DeclaredRepo: "acme/widgets",
		GitBranch: "main", GitSHA: strings.Repeat("a", 40),
	}}
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
		t.Fatal(err)
	}
	db := teststore.SQL(t, s)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE recorded_run_updates (id TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER record_existing_run_update AFTER UPDATE ON runs
WHEN OLD.id = 'older' BEGIN INSERT INTO recorded_run_updates(id) VALUES (OLD.id); END`); err != nil {
		t.Fatal(err)
	}
	seed.ID = "newer"
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM recorded_run_updates`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Errorf("seeding one new run rewrote an existing reporting row %d times", writes)
	}
	var identity, sha, ref string
	if err := db.QueryRowContext(t.Context(), `SELECT repo_identity,provenance_sha,provenance_ref FROM runs WHERE id='newer'`).Scan(&identity, &sha, &ref); err != nil {
		t.Fatal(err)
	}
	wantIdentity, err := sourceurl.RoutingIdentity("gh:1", "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if identity != wantIdentity || sha != seed.GitSHA || ref != seed.GitBranch {
		t.Errorf("seeded identity/provenance = %q/%q/%q, want %q/%q/%q", identity, sha, ref, wantIdentity, seed.GitSHA, seed.GitBranch)
	}
}

func TestFixtureExecStillProjectsChangedRunIdentity(t *testing.T) {
	s, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seed := store.Run{RunSummary: runstate.RunSummary{ID: "report", Pipeline: "build", Status: "success", DeclaredRepo: "acme/widgets"}}
	if err := teststore.SeedRun(s, t.Context(), store.DefaultTeam, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.Exec(t.Context(), s, `UPDATE runs SET repo_display=? WHERE id=?`, "acme/renamed", seed.ID); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := teststore.SQL(t, s).QueryRowContext(t.Context(), `SELECT repo_identity FROM runs WHERE id=?`, seed.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want, err := sourceurl.RoutingIdentity("gh:1", "acme/renamed")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("fixture SQL left routing identity %q, want %q", got, want)
	}
}
