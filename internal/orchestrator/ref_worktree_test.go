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

func submitTrigger(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "p", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
}

func TestSweepRefWorktreesReclaimsAFinishedTrigger(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-term")
	submitTrigger(t, st, "run-term")
	if err := st.FinishTrigger(ctx, "run-term"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	n, err := SweepRefWorktrees(ctx, p, st, nil)
	if err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("a finished trigger's worktree survived the sweep")
	}
}

func TestSweepRefWorktreesKeepsAClaimedTrigger(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-live")
	submitTrigger(t, st, "run-live")
	if _, err := st.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}

	if n, err := SweepRefWorktrees(ctx, p, st, nil); err != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the sweep deleted a worktree a claimed trigger was executing in")
	}
}

func TestSweepRefWorktreesKeepsAWorktreeAQueuedTriggerWillReach(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-requeued")
	submitTrigger(t, st, "run-requeued")

	if n, err := SweepRefWorktrees(ctx, p, st, nil); err != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a claimable trigger's tree executes again, so reclaiming it drops work: %v", err)
	}
}

func TestSweepRefWorktreesKeepsAHeldWorktreeWhateverItsTriggerSays(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-held")
	submitTrigger(t, st, "run-held")
	if err := st.FinishTrigger(ctx, "run-held"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	hold, held, err := HoldRefWorktree(p, "run-held")
	if err != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, err)
	}
	t.Cleanup(func() { _ = ReleaseRefWorktree(hold) })

	if n, serr := SweepRefWorktrees(ctx, p, st, nil); serr != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, serr)
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Fatalf("a finished trigger says the work ended, but a live process still holds this "+
			"tree and its writes are below the top level where no timestamp shows them: %v", serr)
	}
}

func TestSweepRefWorktreesReclaimsAWorktreeOnceItsHolderIsGone(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()
	dir := buildWorktree(t, p, repo, "run-released")
	submitTrigger(t, st, "run-released")
	if err := st.FinishTrigger(ctx, "run-released"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	hold, held, err := HoldRefWorktree(p, "run-released")
	if err != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, err)
	}
	if rerr := ReleaseRefWorktree(hold); rerr != nil {
		t.Fatalf("ReleaseRefWorktree: %v", rerr)
	}

	n, serr := SweepRefWorktrees(ctx, p, st, nil)
	if serr != nil || n != 1 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 1, nil", n, serr)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("a released hold leaves nothing owning the tree, so it must be reclaimed")
	}
}

func TestHoldRefWorktreeRefusesATreeAnotherHolderOwns(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	first, held, err := HoldRefWorktree(p, "run-contended")
	if err != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, err)
	}
	t.Cleanup(func() { _ = ReleaseRefWorktree(first) })

	if _, second, serr := HoldRefWorktree(p, "run-contended"); serr != nil || second {
		t.Fatalf("HoldRefWorktree = %v, %v; a second holder took a tree already in use", second, serr)
	}
}

func TestSweepRefWorktreesSparesASubmissionStillInFlight(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	hold, held, herr := HoldRefWorktree(p, "run-inflight")
	if herr != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, herr)
	}
	t.Cleanup(func() { _ = ReleaseRefWorktree(hold) })
	dir := buildWorktree(t, p, repo, "run-inflight")

	if n, err := SweepRefWorktrees(context.Background(), p, st, nil); err != nil || n != 0 {
		t.Fatalf("SweepRefWorktrees = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the sweep raced a submission and deleted its worktree before the trigger row landed")
	}
}

func TestSweepRefWorktreesReclaimsAnAbandonedSubmission(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	dir := buildWorktree(t, p, repo, "run-abandoned")

	n, err := SweepRefWorktrees(context.Background(), p, st, nil)
	if err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("a worktree whose submission never wrote a trigger survived")
	}
}

func TestSweepRefWorktreesFailsClosedWhenTheStoreCannotAnswer(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	dir := buildWorktree(t, p, repo, "run-unanswerable")
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	n, err := SweepRefWorktrees(context.Background(), p, st, nil)
	if err == nil {
		t.Error("the sweep reported success while the store could not say whether the run had ended")
	}
	if n != 0 {
		t.Errorf("reclaimed %d worktrees without a verdict on any of them", n)
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Error("a worktree was deleted on a store the sweep could not read")
	}
}

func TestHoldRefWorktreeRefusesARunIDThatLeavesTheRoot(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	for _, runID := range []string{"", ".", "..", "../escaped", "nested/id"} {
		hold, held, err := HoldRefWorktree(p, runID)
		if err == nil || held {
			t.Errorf("run id %q took a hold outside %s", runID, p.RefWorktreesDir())
			_ = ReleaseRefWorktree(hold)
		}
	}
}

func TestSweepRefWorktreesKeepsGoingPastAWorktreeItCannotJudge(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()

	reclaimable := buildWorktree(t, p, repo, "run-zzz")
	submitTrigger(t, st, "run-zzz")
	if err := st.FinishTrigger(ctx, "run-zzz"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	poison := buildWorktree(t, p, repo, "run-aaa")
	submitTrigger(t, st, "run-aaa")
	if err := st.FinishTrigger(ctx, "run-aaa"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
	if err := os.Mkdir(poison+refWorktreeLeaseSuffix, 0o700); err != nil {
		t.Fatal(err)
	}

	n, err := SweepRefWorktrees(ctx, p, st, nil)
	if err == nil {
		t.Error("the sweep reported success while one worktree could not be judged")
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	if _, serr := os.Stat(reclaimable); !os.IsNotExist(serr) {
		t.Error("one unreadable lease stopped the sweep reaching a worktree it could reclaim")
	}
}
