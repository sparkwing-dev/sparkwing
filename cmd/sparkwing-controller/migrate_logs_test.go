package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMigrateLogsNeedsTheServicesRoot(t *testing.T) {
	err := runMigrateLogs(nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("migrate-logs without --root = %v, want a refusal naming it", err)
	}
}

var migratedRunFinished = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func logsVolume(t *testing.T, withRun bool) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	root = t.TempDir()
	run := filepath.Join(root, "runs", "run-1")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "build.log"), []byte("line 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !withRun {
		return home, root
	}
	st := openState(t)
	defer func() { _ = st.Close() }()
	tn, err := st.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	finished := migratedRunFinished
	if err := tn.CreateTriggerWithRun(context.Background(),
		store.Trigger{ID: "run-1", Pipeline: "build", CreatedAt: finished.Add(-time.Minute)},
		store.Run{ID: "run-1", Pipeline: "build", Status: "success", StartedAt: finished.Add(-time.Minute), FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	return home, root
}

func openState(t *testing.T) *store.Store {
	t.Helper()
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := openControllerStore(context.Background(), p.StateDB(), "")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A dry run over a logs volume counts its logs, writes nothing beside the
// controller's state database, and never opens the database, whose schema
// opening it would upgrade.
func TestMigrateLogsDryRunCountsAndWritesNothing(t *testing.T) {
	home, root := logsVolume(t, false)
	var out bytes.Buffer
	if err := runMigrateLogs([]string{"--root", root, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would convert 1 run(s), 1 attempt(s), 7 byte(s)") {
		t.Fatalf("dry run said %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "lo")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote under the state directory: %v", err)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDB()); !os.IsNotExist(err) {
		t.Fatalf("dry run opened the state database: %v", err)
	}
}

// A converted attempt is recorded under its run as last written when the
// run finished, so the storage pass prunes it in the order of its age.
func TestMigrateLogsRecordsEachConvertedAttempt(t *testing.T) {
	_, root := logsVolume(t, true)
	var out bytes.Buffer
	if err := runMigrateLogs([]string{"--root", root}, &out); err != nil {
		t.Fatalf("migrate-logs = %v: %s", err, out.String())
	}
	st := openState(t)
	defer func() { _ = st.Close() }()
	recs, err := st.RunNodeLogs(context.Background(), store.DefaultTeam, "run-1")
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v; want one", recs, err)
	}
	rec := recs[0]
	if rec.NodeID != "build" || rec.SealedAt.IsZero() || rec.StoredBytes <= 0 || !rec.UpdatedAt.Equal(migratedRunFinished) {
		t.Fatalf("record = %+v; want build, sealed, its stored bytes, last written when the run finished", rec)
	}
	if prunable, err := st.PrunableNodeLogs(context.Background(), store.DefaultTeam, 10); err != nil || len(prunable) != 1 {
		t.Fatalf("prunable = %+v, %v; want the converted attempt", prunable, err)
	}
}

// A run the controller's database does not hold is named and not
// converted, since nothing would read, prune or delete its logs.
func TestMigrateLogsRefusesARunTheControllerDoesNotHold(t *testing.T) {
	_, root := logsVolume(t, false)
	var out bytes.Buffer
	err := runMigrateLogs([]string{"--root", root}, &out)
	if err == nil || !strings.Contains(out.String(), "run run-1:") || !strings.Contains(out.String(), "converted 0 run(s)") {
		t.Fatalf("migrate-logs = %v: %s; want run-1 named and nothing converted", err, out.String())
	}
}
