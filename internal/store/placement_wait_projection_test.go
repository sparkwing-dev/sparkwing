package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
