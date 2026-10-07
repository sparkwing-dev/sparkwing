package opsview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A store shared by several teams holds runs the default team cannot read; a
// run directory of such a run is not dangling, and its trigger's worktree is
// not reclaimed while the trigger is live.
func TestDoctorReadsRunsAndTriggersOfEveryTeam(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := acme.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-acme", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-acme", Pipeline: "deploy", Status: "running", CreatedAt: now, StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	dirs := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirs, "run-acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	settled := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dirs, "run-acme"), settled, settled); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dirs)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	report := DoctorReport{}
	if err := diagnoseDanglingRunDirs(ctx, st, root, true, true, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.DanglingRunDirs) != 0 {
		t.Fatalf("dangling run dirs = %v, want acme's run kept", report.DanglingRunDirs)
	}
	stale, err := scanUnreclaimedRefWorktrees(ctx, st, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale worktrees = %v, want acme's live trigger kept", stale)
	}
}
