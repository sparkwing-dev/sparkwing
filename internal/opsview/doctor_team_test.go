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

// A worktree belongs to its trigger; another team's run under the same id
// does not make a live trigger's worktree reclaimable.
func TestDoctorKeepsALiveTriggersWorktreeWhenAnotherTeamsRunSharesItsID(t *testing.T) {
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
	if err := st.CreateRun(ctx, store.Run{ID: "shared", Pipeline: "deploy", Status: "running", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := acme.CreateTrigger(ctx, store.Trigger{ID: "shared", Pipeline: "deploy", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	dirs := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirs, "shared"), 0o700); err != nil {
		t.Fatal(err)
	}
	settled := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dirs, "shared"), settled, settled); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dirs)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	stale, err := scanUnreclaimedRefWorktrees(ctx, st, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale worktrees = %v, want acme's live trigger kept", stale)
	}
}

// Deleting a spawned child run keeps its trigger for lineage; the run's
// directory is still dangling once no run row is left.
func TestDoctorReclaimsADeletedChildRunsDirectory(t *testing.T) {
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
		store.Trigger{ID: "run-child", Pipeline: "deploy", ParentRunID: "run-parent", ParentNodeID: "spawn", CreatedAt: now},
		store.Run{ID: "run-child", Pipeline: "deploy", Status: "success", CreatedAt: now, StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	if err := acme.DeleteRun(ctx, "run-child"); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GetTrigger(ctx, "run-child"); err != nil {
		t.Fatalf("child trigger after DeleteRun: %v, want it kept for lineage", err)
	}
	dirs := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirs, "run-child"), 0o700); err != nil {
		t.Fatal(err)
	}
	settled := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dirs, "run-child"), settled, settled); err != nil {
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
	if len(report.DanglingRunDirs) != 1 || report.DanglingRunDirs[0] != "run-child" {
		t.Fatalf("dangling run dirs = %v, want the deleted child run's", report.DanglingRunDirs)
	}
}
