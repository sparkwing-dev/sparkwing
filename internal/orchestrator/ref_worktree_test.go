package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func worktreeRegistered(t *testing.T, repo, dir string) bool {
	t.Helper()
	return strings.Contains(runGitFixture(t, repo, "worktree", "list"), dir)
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func buildWorktree(t *testing.T, p paths.Paths, repo, runID string) string {
	t.Helper()
	rev, err := ResolveRefCommit(context.Background(), repo, "HEAD", nil)
	if err != nil {
		t.Fatalf("ResolveRefCommit: %v", err)
	}
	dir, err := CreateRefWorktree(context.Background(), p, repo, rev, runID, nil)
	if err != nil {
		t.Fatalf("CreateRefWorktree: %v", err)
	}
	return dir
}

func TestCreateRefWorktreeChecksOutTheResolvedCommitAndPersists(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}

	dir := buildWorktree(t, p, repo, "run-1")

	if info, err := os.Stat(filepath.Join(dir, ".sparkwing")); err != nil || !info.IsDir() {
		t.Fatalf("worktree has no .sparkwing directory: %v", err)
	}
	if got := headCommit(t, dir); got != headCommit(t, repo) {
		t.Errorf("worktree is at %s, want %s", got, headCommit(t, repo))
	}
	if !worktreeRegistered(t, repo, dir) {
		t.Error("worktree is not registered with its origin repository")
	}
}

func TestCreateRefWorktreeRefusesACommitWithNoProject(t *testing.T) {
	repo := gitRepoWithProject(t, false)
	p := paths.Paths{Root: t.TempDir()}
	rev := headCommit(t, repo)

	dir, err := CreateRefWorktree(context.Background(), p, repo, rev, "run-2", nil)
	if err == nil {
		t.Fatal("a commit with no .sparkwing directory was accepted")
	}
	if !strings.Contains(err.Error(), ".sparkwing") {
		t.Errorf("error %q does not name the missing directory", err)
	}
	if dir != "" {
		t.Errorf("dir = %q, want empty on refusal", dir)
	}
	if _, serr := os.Stat(p.RefWorktreeDir("run-2")); !os.IsNotExist(serr) {
		t.Error("a refused submission left its worktree behind")
	}
}

func TestRemoveRefWorktreeClearsTreeAndRegistration(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	dir := buildWorktree(t, p, repo, "run-3")

	if err := RemoveRefWorktree(context.Background(), p, dir, nil); err != nil {
		t.Fatalf("RemoveRefWorktree: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("worktree directory survived removal")
	}
	if worktreeRegistered(t, repo, dir) {
		t.Error("registration survived removal, so the path cannot be reused")
	}
}

func TestRemoveRefWorktreeRefusesAPathOutsideItsRoot(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	outside := t.TempDir()
	keep := filepath.Join(outside, "work.txt")
	if err := os.WriteFile(keep, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{outside, p.RefWorktreesDir(), filepath.Join(p.RefWorktreesDir(), "..")} {
		if err := RemoveRefWorktree(context.Background(), p, dir, nil); err == nil {
			t.Errorf("removing %q was allowed", dir)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a directory outside the root was deleted: %v", err)
	}
}

func TestRemoveRefWorktreeDeletesTheTreeWhenTheOriginIsGone(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	dir := buildWorktree(t, p, repo, "run-4")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	if err := RemoveRefWorktree(context.Background(), p, dir, nil); err != nil {
		t.Fatalf("RemoveRefWorktree with no origin repository: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the tree survived when git could not deregister it")
	}
}

func TestSweepRefWorktreesReclaimsATerminalRun(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-term")
	if err := st.CreateRun(ctx, store.Run{ID: "run-term", Pipeline: "p", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	n, err := SweepRefWorktrees(ctx, p, st, nil)
	if err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("a finished run's worktree survived the sweep")
	}
}

func TestSweepRefWorktreesKeepsARunningRun(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-live")
	if err := st.CreateRun(ctx, store.Run{ID: "run-live", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if n, err := SweepRefWorktrees(ctx, p, st, nil); err != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the sweep deleted a worktree its run was still executing")
	}
}

func TestSweepRefWorktreesSparesASubmissionStillInFlight(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	dir := buildWorktree(t, p, repo, "run-inflight")

	if n, err := SweepRefWorktrees(context.Background(), p, st, nil); err != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the sweep raced a submission and deleted its worktree before the run row landed")
	}
}

func TestSweepRefWorktreesReclaimsAnAbandonedSubmission(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	dir := buildWorktree(t, p, repo, "run-abandoned")
	stale := time.Now().Add(-2 * refWorktreeAbsentRunGrace)
	if err := os.Chtimes(dir, stale, stale); err != nil {
		t.Fatal(err)
	}

	n, err := SweepRefWorktrees(context.Background(), p, st, nil)
	if err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("a worktree whose submission never produced a run survived")
	}
}

func TestCreateRefWorktreeRefusesARunIDThatEscapesTheRoot(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	rev := headCommit(t, repo)

	escapes := []string{"..", filepath.Join("..", "escaped"), "."}
	for _, runID := range escapes {
		if _, err := CreateRefWorktree(context.Background(), p, repo, rev, runID, nil); err == nil {
			t.Errorf("run id %q built a worktree outside the root", runID)
		}
	}
}
