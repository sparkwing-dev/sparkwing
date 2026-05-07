package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// stubRunLister returns a canned list of runs regardless of filter.
// Good enough for GCWarmRoot's one call.
type stubRunLister struct {
	runs []*store.Run
	err  error
}

func (s *stubRunLister) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.runs, nil
}

func mustTouch(t *testing.T, path string, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGCWarmRoot_SweepsByAge(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// git/: old dir should go, fresh dir should stay. Set the dir's
	// mtime *after* writing its contents -- creating files inside a
	// dir bumps the parent's mtime on most filesystems.
	oldGit := filepath.Join(root, "git", "old-repo")
	mustTouch(t, filepath.Join(oldGit, "HEAD"), "ref: refs/heads/main\n", now.Add(-30*24*time.Hour))
	if err := os.Chtimes(oldGit, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	freshGit := filepath.Join(root, "git", "fresh-repo")
	mustTouch(t, filepath.Join(freshGit, "HEAD"), "ref: refs/heads/main\n", now.Add(-1*time.Hour))
	if err := os.Chtimes(freshGit, now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// tmp/: old file + fresh file.
	mustTouch(t, filepath.Join(root, "tmp", "old.log"), "stale data", now.Add(-48*time.Hour))
	mustTouch(t, filepath.Join(root, "tmp", "fresh.log"), "recent data", now.Add(-1*time.Hour))

	stats, err := GCWarmRoot(context.Background(), root, nil, silentLogger())
	if err != nil {
		t.Fatalf("GCWarmRoot: %v", err)
	}

	if stats.GitDirsRemoved != 1 {
		t.Errorf("GitDirsRemoved: got %d, want 1", stats.GitDirsRemoved)
	}
	if stats.TmpEntriesRemoved != 1 {
		t.Errorf("TmpEntriesRemoved: got %d, want 1", stats.TmpEntriesRemoved)
	}
	if stats.BytesFreed == 0 {
		t.Errorf("BytesFreed: got 0, want > 0")
	}
	if _, err := os.Stat(oldGit); !os.IsNotExist(err) {
		t.Errorf("old git dir should be gone; stat err=%v", err)
	}
	if _, err := os.Stat(freshGit); err != nil {
		t.Errorf("fresh git dir should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", "old.log")); !os.IsNotExist(err) {
		t.Errorf("old tmp file should be gone; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", "fresh.log")); err != nil {
		t.Errorf("fresh tmp file should remain: %v", err)
	}
}

func TestGCWarmRoot_RemovesTerminalRunDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// Two run dirs on disk: one the controller says is terminal + old,
	// one terminal + within grace, one not listed at all.
	oldRun := filepath.Join(root, "runs", "run-old")
	mustMkdir(t, oldRun, now)
	mustTouch(t, filepath.Join(oldRun, "stdout.log"), "output", now)

	graceRun := filepath.Join(root, "runs", "run-grace")
	mustMkdir(t, graceRun, now)

	unknownRun := filepath.Join(root, "runs", "run-unknown")
	mustMkdir(t, unknownRun, now)

	finishedOld := now.Add(-2 * time.Hour)
	finishedGrace := now.Add(-10 * time.Minute)

	ctrl := &stubRunLister{runs: []*store.Run{
		{ID: "run-old", Status: "success", FinishedAt: &finishedOld},
		{ID: "run-grace", Status: "failed", FinishedAt: &finishedGrace},
	}}

	stats, err := GCWarmRoot(context.Background(), root, ctrl, silentLogger())
	if err != nil {
		t.Fatalf("GCWarmRoot: %v", err)
	}

	if stats.RunDirsRemoved != 1 {
		t.Errorf("RunDirsRemoved: got %d, want 1", stats.RunDirsRemoved)
	}
	if _, err := os.Stat(oldRun); !os.IsNotExist(err) {
		t.Errorf("old terminal run should be gone; stat err=%v", err)
	}
	if _, err := os.Stat(graceRun); err != nil {
		t.Errorf("within-grace run should remain: %v", err)
	}
	if _, err := os.Stat(unknownRun); err != nil {
		t.Errorf("unknown-to-controller run should remain: %v", err)
	}
}

func TestGCWarmRoot_MissingRootIsNotAnError(t *testing.T) {
	_, err := GCWarmRoot(context.Background(), filepath.Join(t.TempDir(), "no-such-dir"), nil, silentLogger())
	if err != nil {
		t.Fatalf("missing root should not error; got %v", err)
	}
}
