package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The scriptable exit contract must be read from the store the status
// came from. A machine that shares a profile's store but never ran the
// pipeline has nothing in its own SQLite, and deriving the exit code
// from there reds every CI step that shells out to `runs status`.
func TestRunStatus_ReadsThroughProfileNotLocalStore(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared.db")
	st, err := store.Open(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	const runID = "run-20260101-000000-abcd"
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", TriggerSource: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatal(err)
	}

	// A different home entirely: this machine has its own empty state.db.
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	p := &profile.Profile{
		Name:  "bucket",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: shared},
	}

	got, err := orchestrator.RunStatus(ctx, paths, p, runID)
	if err != nil {
		t.Fatalf("RunStatus through the profile: %v", err)
	}
	if got != "success" {
		t.Errorf("status: got %q, want success", got)
	}

	// Without the profile the run is genuinely absent, which is the
	// answer the old code gave for every profile-routed read.
	if _, err := orchestrator.RunStatus(ctx, paths, nil, runID); err == nil {
		t.Error("expected the local store to report the run missing")
	}
}

// A failed run still exits non-zero: the fix moves where the status is
// read from, not what a status means.
func TestRunStatus_FailedRunKeepsItsStatus(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared.db")
	st, err := store.Open(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	const runID = "run-20260101-000000-bcde"
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", TriggerSource: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, runID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}

	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	p := &profile.Profile{
		Name:  "bucket",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: shared},
	}
	got, err := orchestrator.RunStatus(ctx, paths, p, runID)
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if got != "failed" {
		t.Errorf("status: got %q, want failed", got)
	}
}
