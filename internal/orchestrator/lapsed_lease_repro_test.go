package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDispatchContextCarriesTheClaimItRunsUnder(t *testing.T) {
	fence, ok := store.TriggerClaimFenceFromContext(
		DispatchContext(context.Background(), &store.Trigger{ID: "run-ctx", ClaimSeq: 7}))
	if !ok {
		t.Fatal("a dispatch context with no claim fence lets a superseded write land unrefused")
	}
	if fence.ClaimGeneration != 7 {
		t.Errorf("ClaimGeneration = %d, want 7", fence.ClaimGeneration)
	}
}

func TestAClaimedTriggerRefusesADispatchItCannotHoldTheTreeFor(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	home := t.TempDir()
	p := paths.Paths{Root: home}
	st := testStore(t)
	ctx := context.Background()

	dir := buildWorktree(t, p, repo, "run-contended")
	rival, held, herr := HoldRefWorktree(p, "run-contended")
	if herr != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, herr)
	}
	t.Cleanup(func() { _ = ReleaseRefWorktree(rival) })

	submitTrigger(t, st, "run-contended")
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	claimed.TriggerEnv = map[string]string{SubmitRepoDirKey: dir}

	runClaimedTrigger(ctx, st, claimed, slog.New(slog.DiscardHandler), home, time.Minute)

	run, gerr := st.GetRun(ctx, "run-contended")
	if gerr != nil {
		t.Fatalf("GetRun: %v", gerr)
	}
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed: a dispatch that cannot hold its tree must refuse "+
			"rather than execute one another process may reclaim", run.Status)
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Errorf("the refused dispatch removed the tree its rival is holding: %v", serr)
	}
}

func TestAClaimedTriggerHoldsItsTreeAgainstTheSweep(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	home := t.TempDir()
	p := paths.Paths{Root: home}
	st := testStore(t)
	ctx := context.Background()

	dir := buildWorktree(t, p, repo, "run-dispatching")
	submitTrigger(t, st, "run-dispatching")
	if err := CaptureSubmissionEnvironment(home, "run-dispatching", []string{"PATH=" + os.Getenv("PATH")}, nil); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	claimed.TriggerEnv = map[string]string{SubmitRepoDirKey: dir, SubmissionEnvironmentCapturedKey: "1"}

	var swept int
	var sweepErr error
	dispatched := false
	origin := dispatchLocalTriggerFn
	dispatchLocalTriggerFn = func(
		context.Context, *store.Trigger, *slog.Logger, []string,
	) error {
		dispatched = true
		if ferr := st.FinishTrigger(ctx, "run-dispatching"); ferr != nil {
			sweepErr = ferr
			return ferr
		}
		swept, sweepErr = SweepRefWorktrees(ctx, p, st, nil)
		return sweepErr
	}
	t.Cleanup(func() { dispatchLocalTriggerFn = origin })

	runClaimedTrigger(ctx, st, claimed, slog.New(slog.DiscardHandler), home, time.Minute)

	if !dispatched {
		t.Fatal("claimed trigger was refused before dispatch")
	}
	if sweepErr != nil {
		t.Fatalf("SweepRefWorktrees during dispatch: %v", sweepErr)
	}
	if swept != 0 {
		t.Errorf("the sweep reclaimed %d worktrees while a dispatch was executing in one; "+
			"its trigger reads finished, so the hold is the only thing left to spare it", swept)
	}
}
