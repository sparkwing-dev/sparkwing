package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPlacementWaitProjectsEachTierFromCommittedState(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	mac := f.SelfHosted(t, "build-mac")
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second, "os=darwin"), cloud(0))})
	f.At(-time.Second)
	f.PollLabels(t, mac, "os=darwin")
	f.At(time.Second)
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.PlacementWaits) != 1 {
		t.Fatalf("waits=%+v", view.PlacementWaits)
	}
	wait := view.PlacementWaits[0]
	if wait.JobID != "job" || len(wait.Tiers) != 2 || len(wait.Tiers[0].Live) != 1 || wait.Tiers[0].Live[0] != "build-mac" || wait.Tiers[0].OpensIn != 0 {
		t.Fatalf("team tier=%+v", wait.Tiers)
	}
	if wait.Tiers[1].OpensIn != 4*time.Second || wait.Tiers[1].Unavailable != "" {
		t.Fatalf("cloud tier=%+v, want it to open in 4s", wait.Tiers[1])
	}
}

func TestPlacementWaitIgnoresATierTheRankingExcludes(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	if err := f.Rank(t, f.Admin, "cloud"); err != nil {
		t.Fatal(err)
	}
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second, "os=darwin"), cloud(0))})
	f.RequireOnJobs(t, "class=cloud,os=darwin")
	f.At(-time.Second)
	f.PollLabels(t, f.Self, "os=darwin")
	f.At(time.Second)
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err != nil || len(view.PlacementWaits) != 1 {
		t.Fatalf("waits=%+v %v", view.PlacementWaits, err)
	}
	if tiers := view.PlacementWaits[0].Tiers; tiers[1].OpensIn != 0 || tiers[0].Unavailable == "" {
		t.Fatalf("a runner the ranking excludes shows as holding Cloud: %+v", tiers)
	}
	wantJob(t, f, f.Launcher)
}

func TestPlacementWaitUsesClaimLiveness(t *testing.T) {
	for _, test := range []struct {
		name     string
		liveness time.Duration
		held     bool
	}{
		{"short", 30 * time.Second, false},
		{"long", 90 * time.Second, true},
		{"default", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := rankingFixture(t, true)
			finishRankingTrigger(t, f)
			f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(time.Hour), cloud(0))})
			f.At(-45 * time.Second)
			f.Poll(t, f.Self, capability.UnitNode)
			f.At(time.Second)
			ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Liveness: test.liveness})
			tenant, err := f.Store.ForTeam(ctx, "a")
			if err != nil {
				t.Fatal(err)
			}
			view, err := tenant.ReadRunView(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			if len(view.PlacementWaits) != 1 {
				t.Fatalf("waits=%+v", view.PlacementWaits)
			}
			tiers := view.PlacementWaits[0].Tiers
			if live, held := len(tiers[0].Live) > 0, tiers[1].OpensIn > 0; live != test.held || held != test.held {
				t.Fatalf("live=%v held=%v want %v: %+v", live, held, test.held, tiers)
			}
			f.Poll(t, f.Launcher, capability.UnitNode)
			claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"team-4cpu"}})
			if test.held {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("held claim=%+v err=%v", claim, err)
				}
			} else if err != nil || claim.NodeID != "job" {
				t.Fatalf("open claim=%+v err=%v", claim, err)
			}
		})
	}
}
