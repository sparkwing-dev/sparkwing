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

func claimNode(ctx context.Context, f *store.RankingFixture, prefix string) (store.UnitClaim, error) {
	return f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: prefix, Kinds: nodeKinds, Labels: []string{"team-4cpu"}})
}

func TestEmptyClaimPollWorkDoesNotGrowWithTheTeamsLiveRunners(t *testing.T) {
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
	if alone.Statements > 20 {
		t.Fatalf("an empty poll ran the full claim search: %+v", alone)
	}
	if crowded != alone {
		t.Fatalf("an empty poll's work grew with the team's live runners: alone %+v, crowded %+v", alone, crowded)
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
