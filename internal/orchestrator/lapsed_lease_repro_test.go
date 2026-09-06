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

func TestALapsedLeaseDoesNotFinishALiveRedispatch(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()

	dir := buildWorktree(t, p, repo, "run-live")
	submitTrigger(t, st, "run-live")
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-live", Pipeline: "p", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if _, err := st.ClaimNextTrigger(ctx, time.Millisecond); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	requeueExpiredClaims(ctx, st, newInFlightSet(), slog.New(slog.DiscardHandler))

	after, err := st.GetTrigger(ctx, "run-live")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if after.IsFinished() {
		t.Errorf("a lapsed lease finished a trigger whose earlier attempt left the terminal run row; "+
			"the dispatch holding the claim is still executing, and everything keyed to a finished "+
			"trigger is now reclaimable under it (status %q)", after.Status)
	}

	if _, err := SweepRefWorktrees(ctx, p, st, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if _, serr := os.Stat(dir); os.IsNotExist(serr) {
		t.Fatal("the sweep removed the worktree a live dispatch is executing in")
	}
}

func TestALapsedLeaseFinishesAClaimWhoseRunEndedUnderIt(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	submitTrigger(t, st, "run-over")
	if _, err := st.ClaimNextTrigger(ctx, time.Millisecond); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-over", Pipeline: "p", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, "run-over", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	requeueExpiredClaims(ctx, st, newInFlightSet(), slog.New(slog.DiscardHandler))

	after, err := st.GetTrigger(ctx, "run-over")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if !after.IsFinished() {
		t.Fatalf("a lapsed claim whose own run ended was left at %q, so nothing reclaims what it "+
			"holds and the queue keeps a trigger no dispatch will finish", after.Status)
	}
}
