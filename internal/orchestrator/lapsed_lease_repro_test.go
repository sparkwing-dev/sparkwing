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

func TestASupersededDispatchFinishingDoesNotReclaimTheLiveTree(t *testing.T) {
	repo := gitRepoWithProject(t, true)
	p := paths.Paths{Root: t.TempDir()}
	st := testStore(t)
	ctx := context.Background()

	dir := buildWorktree(t, p, repo, "run-superseded")
	submitTrigger(t, st, "run-superseded")
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-superseded", Pipeline: "p", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.ClaimNextTrigger(ctx, time.Millisecond); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	hold, held, herr := HoldRefWorktree(p, "run-superseded")
	if herr != nil || !held {
		t.Fatalf("HoldRefWorktree = %v, %v; want a hold", held, herr)
	}
	t.Cleanup(func() { _ = ReleaseRefWorktree(hold) })

	if err := st.FinishRun(ctx, "run-superseded", "failed", "the dispatch this claim superseded"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	requeueExpiredClaims(ctx, st, newInFlightSet(), slog.New(slog.DiscardHandler))

	if _, err := SweepRefWorktrees(ctx, p, st, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("SweepRefWorktrees: %v", err)
	}
	if _, serr := os.Stat(dir); os.IsNotExist(serr) {
		t.Fatal("a superseded dispatch's finish moved this trigger to done, and the sweep took " +
			"the tree the live dispatch still holds")
	}
}

func TestASupersededDispatchCannotFinishTheRunItLost(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	submitTrigger(t, st, "run-fenced")
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-fenced", Pipeline: "p", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	first, err := st.ClaimNextTrigger(ctx, time.Millisecond)
	if err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, rerr := st.RequeueUnstartedClaim(ctx, "run-fenced"); rerr != nil {
		t.Fatalf("RequeueUnstartedClaim: %v", rerr)
	}
	if _, cerr := st.ClaimNextTrigger(ctx, time.Minute); cerr != nil {
		t.Fatalf("second ClaimNextTrigger: %v", cerr)
	}

	lost := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: first.ClaimSeq})
	if ferr := st.FinishRun(lost, "run-fenced", "failed", "the claim this dispatch lost"); ferr == nil {
		t.Fatal("a dispatch whose claim was taken still stamped its own outcome on the run the " +
			"current claim is producing")
	}

	run, gerr := st.GetRun(ctx, "run-fenced")
	if gerr != nil {
		t.Fatalf("GetRun: %v", gerr)
	}
	if run.Status != "pending" {
		t.Errorf("run status = %q, want pending: the superseded write must not land", run.Status)
	}
}

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
