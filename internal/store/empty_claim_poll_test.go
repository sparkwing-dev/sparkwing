package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

var nodeKinds = []capability.UnitKind{capability.UnitNode}

func preferRunner(ctx context.Context, liveness time.Duration) context.Context {
	return store.WithClaimPlacement(ctx, store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=runner"}, Hold: time.Hour}, {}}, Liveness: liveness})
}

func claimNode(ctx context.Context, f *store.RankingFixture, prefix string) (store.UnitClaim, error) {
	return f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: prefix, Kinds: nodeKinds, Labels: []string{"team-4cpu"}})
}

func TestEmptyClaimPollTakesNoTeamLockWhateverTheTeamsRunnerCount(t *testing.T) {
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
	if alone.TeamLocks != 0 || alone.WriteTransactions > 1 || alone.Writes > 1 {
		t.Fatalf("an unchanged empty poll locked the team or wrote more than its timestamp: %+v", alone)
	}
	if crowded != alone {
		t.Fatalf("an empty poll's work grew with the team's live runners: alone %+v, crowded %+v", alone, crowded)
	}
}

func TestPreferenceHoldLastsALivenessWindowAfterTheLastOfFrequentPolls(t *testing.T) {
	const liveness = 30 * time.Second
	f := rankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	ctx := preferRunner(t.Context(), liveness)
	last := -time.Second
	for at := last; at < 30*time.Second; at += 2 * time.Second {
		f.At(at)
		f.PollIn(ctx, t, f.Self, 1)
		last = at
	}
	f.At(last + liveness - time.Millisecond)
	if claim, err := claimNode(ctx, f, f.Launcher); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the hold lapsed before a full liveness window had passed since the preferred runner's last poll: %+v %v", claim, err)
	}
	f.At(last + liveness + time.Millisecond)
	if claim, err := claimNode(ctx, f, f.Launcher); err != nil || claim.NodeID != "job" {
		t.Fatalf("the hold outlived the preferred runner's liveness: %+v %v", claim, err)
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
			// safety: the controller refuses an idle-poll suggestion whose jittered interval does not fit twice in liveness.
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

func TestEmptyPollExistenceCheckUsesThePartialClaimIndexesOnPostgres(t *testing.T) {
	plan := store.ClaimableUnitQueuedPlan(t, storetest.OpenPostgres(t), "a")
	t.Log(plan)
	if strings.Contains(plan, "Seq Scan") {
		t.Fatalf("the existence check scans a table instead of a partial claim index:\n%s", plan)
	}
	// safety: a team-prefixed index without the claim predicate would still read the team's whole history.
	for _, index := range []string{"using idx_nodes_claim_", "using idx_eval_units_claimable ", "using idx_triggers_claim_"} {
		if !strings.Contains(plan, index) {
			t.Fatalf("the plan does not use a partial claim index (%s):\n%s", strings.TrimPrefix(index, "using "), plan)
		}
	}
}

func TestAnAwardBetweenAnUnchangedPollsReadAndItsWriteFallsBackToTheLockedWrite(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	f.PollIn(ctx, t, f.Self, 2)
	f.At(2 * time.Second)
	count := f.Store.TestOnlyCountStatements(t)
	slots := 2
	poll := store.UnitDemand{CapabilityPrefix: f.Self, Kinds: nodeKinds, Labels: []string{"team-4cpu"}, FreeSlots: &slots}
	var afterAward int
	if err := f.Store.TestOnlyRecordRunnerPollWith(ctx, poll, func() {
		f.Store.TestOnlyConsumeClaimSlot(ctx, t, f.Self, "a")
		afterAward, _ = f.Store.TestOnlyRunnerPollRow(ctx, t, f.Self)
		count.Take()
	}); err != nil {
		t.Fatal(err)
	}
	took := count.Take()
	if afterAward != 1 || took.TeamLocks == 0 {
		t.Fatalf("the restamp wrote over the award instead of falling back to the locked write: after award %d, %+v", afterAward, took)
	}
}

func TestARevokedCredentialsUnchangedPollDoesNotAdvanceItsLastSeenTime(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	f.PollIn(ctx, t, f.Self, 1)
	_, before := f.Store.TestOnlyRunnerPollRow(ctx, t, f.Self)
	f.At(2 * time.Second)
	slots := 1
	err := f.Store.TestOnlyRecordRunnerPollWith(ctx, store.UnitDemand{CapabilityPrefix: f.Self, Kinds: nodeKinds, Labels: []string{"team-4cpu"}, FreeSlots: &slots}, func() {
		f.Store.TestOnlyRevoke(t, f.Self)
	})
	if _, after := f.Store.TestOnlyRunnerPollRow(ctx, t, f.Self); after != before {
		t.Fatalf("a credential revoked before the restamp had its last-seen time advanced from %d to %d (poll error %v)", before, after, err)
	}
	if err == nil {
		t.Fatal("a poll from a credential revoked mid-poll was recorded")
	}
}

func TestAWithdrawnLabelBetweenAnUnchangedPollsReadAndItsRestampSendsItToTheLockedWrite(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	f.PollIn(ctx, t, f.Personal, 1)
	_, before := f.Store.TestOnlyRunnerPollRow(ctx, t, f.Personal)
	f.At(2 * time.Second)
	slots := 1
	err := f.Store.TestOnlyRecordRunnerPollWith(ctx, store.UnitDemand{CapabilityPrefix: f.Personal, Kinds: nodeKinds, Labels: []string{"team-4cpu"}, FreeSlots: &slots}, func() {
		f.Store.TestOnlyWithdrawRunnerLabels(ctx, t, f.Personal)
	})
	if _, after := f.Store.TestOnlyRunnerPollRow(ctx, t, f.Personal); after != before {
		t.Fatalf("a restamp refreshed an observation whose label was withdrawn before it: last seen %d to %d (poll error %v)", before, after, err)
	}
	if err == nil {
		t.Fatal("a poll advertising a withdrawn label was recorded")
	}
}

func TestACredentialInRotationGraceKeepsTheUnchangedPollFastPath(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	f.PollIn(ctx, t, f.Self, 1)
	f.Store.TestOnlyRevokeAfter(t, f.Self, time.Hour)
	f.At(2 * time.Second)
	count := f.Store.TestOnlyCountStatements(t)
	f.PollIn(ctx, t, f.Self, 1)
	if took := count.Take(); took.TeamLocks != 0 {
		t.Fatalf("a poll from a credential still inside its rotation grace took the locked write: %+v", took)
	}
	if _, seen := f.Store.TestOnlyRunnerPollRow(ctx, t, f.Self); seen != f.ReadyAt.Add(2*time.Second).UnixNano() {
		t.Fatalf("the grace-period poll did not restamp the last-seen time: %d", seen)
	}
}

func TestALabelWithdrawnFromTheRootOfADelegatedPollerSendsItsRestampToTheLockedWrite(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	child, err := f.Store.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: f.Personal}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	poller := child.Capability.Prefix
	f.PollIn(ctx, t, poller, 1)
	_, before := f.Store.TestOnlyRunnerPollRow(ctx, t, poller)
	f.At(2 * time.Second)
	slots := 1
	err = f.Store.TestOnlyRecordRunnerPollWith(ctx, store.UnitDemand{CapabilityPrefix: poller, Kinds: nodeKinds, Labels: []string{"team-4cpu"}, FreeSlots: &slots}, func() {
		f.Store.TestOnlyWithdrawRunnerLabels(ctx, t, f.Personal)
	})
	if _, after := f.Store.TestOnlyRunnerPollRow(ctx, t, poller); after != before {
		t.Fatalf("a delegated poller's restamp ran over its root's withdrawn label: last seen %d to %d (poll error %v)", before, after, err)
	}
	if err == nil {
		t.Fatal("a poll advertising a label its root withdrew was recorded")
	}
}

func TestADelegatedPollerAlwaysTakesTheLockedWrite(t *testing.T) {
	f := rankingFixture(t, true)
	ctx := preferRunner(t.Context(), 30*time.Second)
	f.At(0)
	child, err := f.Store.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: f.Personal}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	f.PollIn(ctx, t, child.Capability.Prefix, 1)
	f.At(2 * time.Second)
	count := f.Store.TestOnlyCountStatements(t)
	f.PollIn(ctx, t, child.Capability.Prefix, 1)
	if took := count.Take(); took.TeamLocks == 0 {
		t.Fatalf("an unchanged poll from a delegated credential skipped the locked write: %+v", took)
	}
}
