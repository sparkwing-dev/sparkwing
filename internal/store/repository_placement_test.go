package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func tiers(steps ...runspec.RepositoryTier) []runspec.RepositoryTier { return steps }

func team(hold time.Duration, labels ...string) runspec.RepositoryTier {
	return runspec.RepositoryTier{Target: runspec.PlacementTeam, Labels: labels, Hold: hold}
}

func cloud(hold time.Duration) runspec.RepositoryTier {
	return runspec.RepositoryTier{Target: runspec.PlacementCloud, Hold: hold}
}

func wantNoJob(t *testing.T, f *store.RankingFixture, prefix string, labels ...string) {
	t.Helper()
	if claim, err := f.ClaimLabels(t, prefix, labels...); !errors.Is(err, store.ErrNotFound) {
		var held *apierr.Error
		if !errors.As(err, &held) || held.Code != apierr.LockHeld {
			t.Fatalf("%s took a job it should wait for or cannot have: %+v %v", prefix, claim, err)
		}
	}
}

func wantJob(t *testing.T, f *store.RankingFixture, prefix string, labels ...string) {
	t.Helper()
	if claim, err := f.ClaimLabels(t, prefix, labels...); err != nil || claim.NodeID != "job" {
		t.Fatalf("%s claim: %+v %v", prefix, claim, err)
	}
}

func TestPlacementTeamTierHoldsCloudUntilItsDeadline(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second), cloud(0))})
	livePolls(t, f, f.Self, f.Launcher)
	f.At(4 * time.Second)
	wantNoJob(t, f, f.Launcher)
	f.At(5 * time.Second)
	wantJob(t, f, f.Launcher)
}

func TestPlacementTeamTierClaimsInsideItsHold(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second), cloud(0))})
	livePolls(t, f, f.Self, f.Launcher)
	f.At(time.Second)
	wantNoJob(t, f, f.Launcher)
	wantJob(t, f, f.Self)
}

func TestPlacementAbsentOrSaturatedTierNeverHolds(t *testing.T) {
	for name, poll := range map[string]func(*store.RankingFixture){
		"absent":    func(*store.RankingFixture) {},
		"stale":     func(f *store.RankingFixture) { f.At(-2 * time.Minute); f.Poll(t, f.Self) },
		"saturated": func(f *store.RankingFixture) { f.At(-time.Second); f.PollWithSlots(t, f.Self, 0) },
		"mismatch":  func(f *store.RankingFixture) { f.At(-time.Second); f.PollLabels(t, f.Self, "os=linux") },
	} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			finishRankingTrigger(t, f)
			f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(time.Hour, "os=darwin"), cloud(0))})
			poll(f)
			f.At(time.Second)
			wantJob(t, f, f.Launcher)
		})
	}
}

func TestPlacementTierLabelsNarrowOnlyThatTier(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	mac := f.SelfHosted(t, "build-mac")
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second, "os=darwin"), team(0))})
	f.At(-time.Second)
	f.PollLabels(t, mac, "os=darwin")
	f.At(time.Second)
	wantNoJob(t, f, f.Self, "os=linux")
	wantJob(t, f, mac, "os=darwin")
}

func TestPlacementListIsTheWholeAllowedSet(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(0))})
	f.At(2 * time.Minute)
	wantNoJob(t, f, f.Launcher)
	wantNoJob(t, f, f.Actions)
	wantJob(t, f, f.Self)
}

func TestPlacementJobOverrideReplacesThePipelineList(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(cloud(0)), Jobs: []runspec.RepositoryJobPlacement{{Job: "job", Prefer: tiers(team(0))}}})
	f.At(2 * time.Minute)
	wantNoJob(t, f, f.Launcher)
	wantJob(t, f, f.Self)
}

func scopedRunner(t *testing.T, f *store.RankingFixture) string {
	t.Helper()
	issued, err := f.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, AllowedLabels: []string{"team-4cpu"}, Authority: &f.Admin.Authority, RegistrationID: "runner:a:scoped", Team: "a",
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "scoped"}, Repositories: []string{"github.com/acme/*"},
		Grants: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Capability.Prefix
}

func TestPlacementEmptyJobListRestoresTeamRanking(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.At(-time.Minute)
	scoped := scopedRunner(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(0)), Jobs: []runspec.RepositoryJobPlacement{{Job: "job", Prefer: tiers()}}})
	livePolls(t, f, scoped, f.Launcher)
	f.At(time.Second)
	wantNoJob(t, f, f.Launcher)
	f.At(6 * time.Second)
	wantJob(t, f, f.Launcher)
}

func TestPlacementListSuppressesTheRankingHold(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.At(-time.Minute)
	scoped := scopedRunner(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(cloud(0), team(0))})
	livePolls(t, f, scoped, f.Launcher)
	f.At(time.Second)
	wantJob(t, f, f.Launcher)
}

func TestPlacementListKeepsTheRankingFilter(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
		t.Fatal(err)
	}
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(0), cloud(0))})
	f.At(2 * time.Minute)
	wantNoJob(t, f, f.Launcher)
	wantJob(t, f, f.Self)
}

func TestPlacementListSuppressesTheControllerPreferenceHold(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	preferred := f.SelfHosted(t, "preferred")
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(0))})
	livePolls(t, f, preferred)
	f.At(time.Second)
	ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=preferred"}, Hold: time.Minute}}, Liveness: time.Minute})
	f.Poll(t, f.Self, capability.UnitNode)
	claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Self, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"team-4cpu"}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("controller preference held an explicit repository list: %+v %v", claim, err)
	}
}

func TestPlacementUnfundedCloudTierNeitherWinsNorBlocksTeam(t *testing.T) {
	f := rankingFixture(t, false)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(cloud(time.Hour), team(0))})
	livePolls(t, f, f.Launcher)
	f.At(time.Second)
	wantNoJob(t, f, f.Launcher)
	wantJob(t, f, f.Self)
}

func TestPlacementNamedTierHoldsOtherTeamRunners(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	builder := f.SelfHosted(t, "builder")
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(runspec.RepositoryTier{Target: "name=builder", Hold: 5 * time.Second}, team(0))})
	livePolls(t, f, builder)
	f.At(time.Second)
	wantNoJob(t, f, f.Self)
	wantJob(t, f, builder)
}

func TestPlacementActionsTierRefusesUnboundActionsRunner(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second), runspec.RepositoryTier{Target: runspec.PlacementGitHubActions, Workflow: ".github/workflows/fallback.yaml", Runner: "ubuntu-latest"})})
	livePolls(t, f, f.Self)
	f.At(time.Second)
	wantNoJob(t, f, f.Actions)
	f.At(5 * time.Second)
	wantNoJob(t, f, f.Actions)
}

func TestPlacementNamedClaimFollowsTheSameHold(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second), cloud(0))})
	livePolls(t, f, f.Self, f.Launcher)
	f.At(time.Second)
	target := &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	if claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Launcher, Target: target, Labels: []string{"team-4cpu"}}); err == nil {
		t.Fatalf("named Cloud claim skipped the team hold: %+v", claim)
	}
	f.At(5 * time.Second)
	if claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Launcher, Target: target, Labels: []string{"team-4cpu"}}); err != nil || claim.NodeID != "job" {
		t.Fatalf("named Cloud claim after the hold: %+v %v", claim, err)
	}
}

func TestPlacementRankingStillFiltersAPinnedListedJob(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
		t.Fatal(err)
	}
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(cloud(0))})
	f.RequireOnJobs(t, "class=cloud")
	f.At(2 * time.Minute)
	wantNoJob(t, f, f.Launcher)
}

func TestPlacementPinnedUnlistedJobStillSkipsTheRanking(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
		t.Fatal(err)
	}
	f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginApp })
	f.RequireOnJobs(t, "class=cloud")
	f.At(2 * time.Minute)
	wantJob(t, f, f.Launcher)
}

func TestPlacementHoldRunsFromTheQueueStartNotARenewedReadyTime(t *testing.T) {
	f := rankingFixture(t, true)
	finishRankingTrigger(t, f)
	f.Place(t, runspec.RepositoryPlacement{Prefer: tiers(team(5*time.Second), cloud(0))})
	if _, err := f.Store.DB().ExecContext(t.Context(), storetest.Rebind(f.Store, `UPDATE nodes SET queue_started_at=?,ready_at=?`), f.ReadyAt.UnixNano(), f.ReadyAt.Add(4*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Self, f.Launcher)
	f.At(4 * time.Second)
	wantNoJob(t, f, f.Launcher)
	f.At(5 * time.Second)
	wantJob(t, f, f.Launcher)
}
