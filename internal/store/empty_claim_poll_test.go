package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

var nodeKinds = []capability.UnitKind{capability.UnitNode}

func preferRunner(ctx context.Context, liveness time.Duration) context.Context {
	return store.WithClaimPlacement(ctx, store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=runner"}, Hold: time.Hour}, {}}, Liveness: liveness})
}

func claimNode(ctx context.Context, f *store.RankingFixture, prefix string) (store.UnitClaim, error) {
	return f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: prefix, Kinds: nodeKinds, Labels: []string{"team-4cpu"}})
}

func TestEmptyClaimPollIsReadOnlyWhateverTheTeamsRunnerCount(t *testing.T) {
	f := rankingFixture(t, true)
	f.ClearQueue(t)
	ctx := t.Context()
	count := f.Store.TestOnlyCountStatements(t)
	emptyPoll := func() store.StatementCount {
		t.Helper()
		count.Take()
		f.PollIn(ctx, t, f.Self, 1)
		if claim, err := claimNode(ctx, f, f.Self); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("empty queue answered %+v %v", claim, err)
		}
		return count.Take()
	}
	f.At(0)
	f.PollIn(ctx, t, f.Self, 1)
	f.At(time.Second)
	alone := emptyPoll()
	others := []string{f.Personal, f.Tablet, f.Second, f.Launcher, f.SelfHosted(t, "builder-1"), f.SelfHosted(t, "builder-2")}
	for _, prefix := range others {
		f.PollIn(ctx, t, prefix, 1)
	}
	f.At(2 * time.Second)
	crowded := emptyPoll()
	t.Logf("empty poll alone %+v, beside %d live runners %+v", alone, len(others), crowded)
	if alone.WriteTransactions != 0 || alone.Writes != 0 || alone.TeamLocks != 0 {
		t.Fatalf("an unchanged empty poll wrote or locked: %+v", alone)
	}
	if crowded != alone {
		t.Fatalf("an empty poll's work grew with the team's live runners: alone %+v, crowded %+v", alone, crowded)
	}
}

func TestUnchangedRunnerPollIsRewrittenWithinAQuarterOfLiveness(t *testing.T) {
	for _, test := range []struct{ liveness, refresh time.Duration }{
		{20 * time.Second, 5 * time.Second},
		{30 * time.Second, 7500 * time.Millisecond},
		{time.Minute, 15 * time.Second},
		{time.Hour, 15 * time.Second},
	} {
		t.Run(test.liveness.String(), func(t *testing.T) {
			f := rankingFixture(t, true)
			ctx := preferRunner(t.Context(), test.liveness)
			count := f.Store.TestOnlyCountStatements(t)
			poll := func(at time.Duration, slots int) store.StatementCount {
				t.Helper()
				f.At(at)
				count.Take()
				f.PollIn(ctx, t, f.Self, slots)
				return count.Take()
			}
			poll(0, 1)
			if got := poll(test.refresh-time.Millisecond, 1); got.Writes != 0 {
				t.Fatalf("an unchanged poll inside the refresh interval wrote: %+v", got)
			}
			if got := poll(test.refresh, 1); got.Writes == 0 {
				t.Fatalf("an unchanged poll at the refresh interval did not write: %+v", got)
			}
			if got := poll(test.refresh+time.Millisecond, 0); got.Writes == 0 {
				t.Fatalf("a changed poll inside the refresh interval did not write: %+v", got)
			}
		})
	}
}

func TestPreferenceHoldStaysWhileThePreferredRunnerPollsAtTheSlowestAdvisedInterval(t *testing.T) {
	for _, liveness := range []time.Duration{20 * time.Second, 30 * time.Second, time.Minute} {
		t.Run(liveness.String(), func(t *testing.T) {
			f := rankingFixture(t, true)
			if err := f.Rank(t, f.Admin); err != nil {
				t.Fatal(err)
			}
			ctx := preferRunner(t.Context(), liveness)
			// The controller refuses an idle-poll suggestion whose jittered interval does not fit twice in the liveness window.
			interval := liveness / 2
			last := -time.Second
			for at := last; at < 3*time.Minute; at += interval {
				f.At(at)
				f.PollIn(ctx, t, f.Self, 1)
				last = at
				f.At(at + interval - time.Millisecond)
				if claim, err := claimNode(ctx, f, f.Launcher); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("the hold lapsed %s after a poll from a preferred runner polling every %s: %+v %v", interval-time.Millisecond, interval, claim, err)
				}
			}
			f.At(last + liveness + time.Second)
			if claim, err := claimNode(ctx, f, f.Launcher); err != nil || claim.NodeID != "job" {
				t.Fatalf("the hold outlived the preferred runner's liveness: %+v %v", claim, err)
			}
		})
	}
}

func TestPreferenceHoldReleasesWhenThePreferredRunnerReportsNoFreeSlot(t *testing.T) {
	f := rankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	ctx := preferRunner(t.Context(), time.Minute)
	f.At(-time.Second)
	f.PollIn(ctx, t, f.Self, 1)
	f.At(0)
	if claim, err := claimNode(ctx, f, f.Launcher); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a live preferred runner with a slot did not hold the job: %+v %v", claim, err)
	}
	f.At(time.Second)
	f.PollIn(ctx, t, f.Self, 0)
	if claim, err := claimNode(ctx, f, f.Launcher); err != nil || claim.NodeID != "job" {
		t.Fatalf("a full preferred runner still held the job: %+v %v", claim, err)
	}
}

func TestQueuedWorkIsAlwaysOfferedToClaimantsThatCanSeeIt(t *testing.T) {
	t.Run("the team's runner sees its plan unit", func(t *testing.T) {
		f := rankingFixture(t, true)
		f.At(time.Minute)
		wantAward(t, f, f.Self, capability.UnitPlan, "")
	})
	t.Run("the team's runner sees its job", func(t *testing.T) {
		f := rankingFixture(t, true)
		finishRankingTrigger(t, f)
		f.At(time.Minute)
		wantAward(t, f, f.Self, capability.UnitNode, "job")
	})
	t.Run("the Cloud launcher sees any team's job", func(t *testing.T) {
		f := rankingFixture(t, true)
		finishRankingTrigger(t, f)
		f.At(time.Minute)
		wantAward(t, f, f.Launcher, capability.UnitNode, "job")
	})
	t.Run("another team's runner sees nothing", func(t *testing.T) {
		f := rankingFixture(t, true)
		finishRankingTrigger(t, f)
		other := f.OtherTeamRunner(t)
		f.At(time.Minute)
		wantNone(t, f, other, capability.UnitNode)
		wantAward(t, f, f.Self, capability.UnitNode, "job")
	})
}
