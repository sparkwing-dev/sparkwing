package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func rankingFixture(t *testing.T, funded bool) *store.RankingFixture {
	t.Helper()
	return storetest.Open(t).TestOnlyRankingFixture(t, funded)
}

func finishRankingTrigger(t *testing.T, f *store.RankingFixture) {
	t.Helper()
	if _, err := f.Store.DB().ExecContext(t.Context(), storetest.Rebind(f.Store, `UPDATE triggers SET status='done',finished_at=?`), f.ReadyAt.UnixNano()); err != nil {
		t.Fatal(err)
	}
}

func livePolls(t *testing.T, f *store.RankingFixture, prefixes ...string) {
	t.Helper()
	f.At(-time.Second)
	for _, prefix := range prefixes {
		f.Poll(t, prefix)
	}
}

func wantAward(t *testing.T, f *store.RankingFixture, prefix string, kind capability.UnitKind, job string) {
	t.Helper()
	claim, err := f.Claim(t, prefix, kind)
	if err != nil || claim.Kind != kind || claim.NodeID != job {
		t.Fatalf("%s claim of %s %q: %+v %v", prefix, kind, job, claim, err)
	}
}

func wantNone(t *testing.T, f *store.RankingFixture, prefix string, kind capability.UnitKind) {
	t.Helper()
	if claim, err := f.Claim(t, prefix, kind); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%s took %s it should wait for or cannot have: %+v %v", prefix, kind, claim, err)
	}
}

func TestRankingTopTierPresentClaimsImmediately(t *testing.T) {
	f := rankingFixture(t, true)
	livePolls(t, f, f.Personal, f.Actions, f.Self, f.Launcher)
	f.At(time.Second)
	wantNone(t, f, f.Self, capability.UnitPlan)
	wantNone(t, f, f.Launcher, capability.UnitPlan)
	wantAward(t, f, f.Personal, capability.UnitPlan, "")
	wantAward(t, f, f.Personal, capability.UnitNode, "job")
}

func TestRankingSeesTheLocalQueue(t *testing.T) {
	f := rankingFixture(t, true)
	local := f.LocalQueue(t)
	if err := f.Rank(t, f.Admin, "self-hosted", "github-actions"); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, local)
	f.At(time.Second)
	wantNone(t, f, f.Actions, capability.UnitPlan)
	wantAward(t, f, local, capability.UnitPlan, "")
}

func TestUnmeteredPushClaimsWithoutCloudFunding(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			f := rankingFixture(t, false)
			f.At(8 * time.Second)
			job := ""
			if kind == capability.UnitNode {
				job = "job"
			}
			wantAward(t, f, f.Actions, kind, job)
		})
	}
}

func TestRankingCreditHoldOnlyWhenCloudCouldTakeTheUnit(t *testing.T) {
	for name, test := range map[string]struct {
		setup func(*testing.T, *store.RankingFixture)
		hold  bool
		cause string
	}{
		"Cloud ranked out": {setup: func(t *testing.T, f *store.RankingFixture) {
			if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
				t.Fatal(err)
			}
		}, cause: "Cloud is off in this team's runner ranking"},
		"a pool pin":          {setup: func(t *testing.T, f *store.RankingFixture) { f.RequireOnJobs(t, "pool=gpu") }, cause: "this job targets pool=gpu only"},
		"Cloud would take it": {setup: func(*testing.T, *store.RankingFixture) {}, hold: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, false)
			finishRankingTrigger(t, f)
			test.setup(t, f)
			f.Poll(t, f.Launcher)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			view, err := tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil || (view.Waiting.CreditHold != "") != test.hold || !test.hold && view.Waiting.NoEligible != test.cause {
				t.Fatalf("waiting unit: %+v %v", view.Waiting, err)
			}
		})
	}
}

func TestRankingHereIsTheSubmittingMachine(t *testing.T) {
	f := rankingFixture(t, true)
	f.Request(t, func(r *runspec.Request) { r.OriginMachine = "laptop" })
	livePolls(t, f, f.Personal, f.Tablet)
	f.At(time.Second)
	wantNone(t, f, f.Tablet, capability.UnitPlan)
	wantAward(t, f, f.Personal, capability.UnitPlan, "")
}

func TestRankingNoneMeansFirstCome(t *testing.T) {
	f := rankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Personal, f.Actions, f.Self)
	f.At(time.Second)
	wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	wantAward(t, f, f.Self, capability.UnitNode, "job")
}

func TestRankingCloudTakesPartOnlyWithBilling(t *testing.T) {
	for _, funded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfunded", true: "funded"}[funded], func(t *testing.T) {
			f := rankingFixture(t, funded)
			if err := f.Rank(t, f.Admin, store.TierCloud, "self-hosted"); err != nil {
				t.Fatal(err)
			}
			livePolls(t, f, f.Launcher)
			f.At(time.Second)
			if !funded {
				wantNone(t, f, f.Launcher, capability.UnitPlan)
				wantAward(t, f, f.Self, capability.UnitPlan, "")
				return
			}
			wantNone(t, f, f.Self, capability.UnitPlan)
			wantAward(t, f, f.Launcher, capability.UnitPlan, "")
		})
	}
}

func TestRankingAdminReordersAndDropsTiers(t *testing.T) {
	f := rankingFixture(t, true)
	var denied *apierr.Error
	if err := f.Rank(t, f.Member, store.TierCloud); !errors.As(err, &denied) {
		t.Fatalf("an editor changed the ranking: %v", err)
	}
	if err := f.Rank(t, f.Admin, store.TierCloud, store.TierCloud); err == nil {
		t.Fatal("accepted a repeated tier")
	}
	if err := f.Rank(t, f.Admin, store.TierCloud, "self-hosted", "personal"); err != nil {
		t.Fatal(err)
	}
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := tenant.TeamSettings(t.Context())
	if err != nil || len(settings.ClaimRanking) != 3 || settings.ClaimRanking[0] != store.TierCloud {
		t.Fatalf("ranking readback: %+v %v", settings, err)
	}
	livePolls(t, f, f.Self, f.Personal, f.Launcher)
	f.At(time.Second)
	wantNone(t, f, f.Actions, capability.UnitPlan)
	wantNone(t, f, f.Self, capability.UnitPlan)
	wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	f.At(time.Minute)
	wantNone(t, f, f.Actions, capability.UnitNode)
	wantAward(t, f, f.Self, capability.UnitNode, "job")
}

func TestRankingLeavesExplicitPlacementsAlone(t *testing.T) {
	t.Run("named runner", func(t *testing.T) {
		f := rankingFixture(t, true)
		f.Request(t, func(r *runspec.Request) {
			r.Dispatch = runspec.DispatchHost
			r.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "runner"}}}
		})
		if err := f.Rank(t, f.Admin, store.TierCloud); err != nil {
			t.Fatal(err)
		}
		livePolls(t, f, f.Personal, f.Launcher)
		f.At(time.Second)
		wantAward(t, f, f.Self, capability.UnitNode, "job")
	})
	t.Run("Cloud pin", func(t *testing.T) {
		f := rankingFixture(t, true)
		f.Request(t, func(r *runspec.Request) {
			r.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"class=cloud"}}}
		})
		if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
			t.Fatal(err)
		}
		livePolls(t, f, f.Self)
		f.At(time.Second)
		wantNone(t, f, f.Self, capability.UnitPlan)
		wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	})
}

func TestRankingLeavesAPinnedJobInsideARunAnywhereRun(t *testing.T) {
	f := rankingFixture(t, true)
	f.RequireOnJobs(t, "name=runner")
	if err := f.Rank(t, f.Admin, store.TierCloud); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Launcher)
	f.At(time.Second)
	wantNone(t, f, f.Self, capability.UnitPlan)
	wantAward(t, f, f.Self, capability.UnitNode, "job")
}

func TestRankingPreservesPreferenceHolds(t *testing.T) {
	f := rankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Self)
	f.At(time.Second)
	f.Poll(t, f.Launcher, capability.UnitNode)
	ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=runner"}, Hold: time.Hour}, {}}, Liveness: time.Hour})
	claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ranked job bypassed its preference hold: %+v %v", claim, err)
	}
}

func TestPreferenceHoldRequiresAnAdmissibleClaimant(t *testing.T) {
	for _, scenario := range []string{"ranking forbids preferred runner", "preferred runner has no free slot"} {
		t.Run(scenario, func(t *testing.T) {
			f := rankingFixture(t, true)
			if scenario == "ranking forbids preferred runner" {
				if err := f.Rank(t, f.Admin, store.TierCloud); err != nil {
					t.Fatal(err)
				}
				f.Poll(t, f.Self, capability.UnitNode)
			} else {
				if err := f.Rank(t, f.Admin); err != nil {
					t.Fatal(err)
				}
				f.PollWithSlots(t, f.Self, 0)
			}
			f.At(time.Second)
			ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=runner"}, Hold: time.Hour}, {}}, Liveness: time.Hour})
			claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}})
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("inadmissible preferred runner held fallback: %+v %v", claim, err)
			}
		})
	}
}

func TestRankingWindowIsPerJob(t *testing.T) {
	f := rankingFixture(t, true)
	f.AddJob(t, "later", f.ReadyAt.Add(4*time.Second))
	livePolls(t, f, f.Personal)
	f.At(6 * time.Second)
	wantAward(t, f, f.Self, capability.UnitNode, "job")
	wantNone(t, f, f.Self, capability.UnitNode)
	f.At(9500 * time.Millisecond)
	wantAward(t, f, f.Self, capability.UnitNode, "later")
}

func TestRankingNoEligibleRunnerNamesTheCause(t *testing.T) {
	for name, test := range map[string]struct {
		setup func(*testing.T, *store.RankingFixture)
		want  string
	}{
		"a dropped tier": {setup: func(t *testing.T, f *store.RankingFixture) {
			if err := f.Rank(t, f.Admin, "here", "personal", "github-actions", "self-hosted"); err != nil {
				t.Fatal(err)
			}
		}, want: "Cloud is off in this team's runner ranking"},
		"several dropped tiers": {setup: func(t *testing.T, f *store.RankingFixture) {
			if err := f.Rank(t, f.Admin, "self-hosted"); err != nil {
				t.Fatal(err)
			}
			f.Poll(t, f.Personal)
			f.Poll(t, f.Actions)
		}, want: "Personal machines, GitHub Actions runners and Cloud are off in this team's runner ranking"},
		"a pinned job": {setup: func(t *testing.T, f *store.RankingFixture) {
			f.RequireOnJobs(t, "pool=gpu")
		}, want: "this job targets pool=gpu only"},
		"nobody online":    {setup: func(*testing.T, *store.RankingFixture) {}, want: "no runner of the allowed tiers (here, personal, github-actions, self-hosted, cloud) is online; start or register a runner"},
		"an online runner": {setup: func(t *testing.T, f *store.RankingFixture) { f.Poll(t, f.Self) }},
	} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			finishRankingTrigger(t, f)
			test.setup(t, f)
			f.At(time.Minute)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			view, err := tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil || view.Waiting.NoEligible != test.want || view.Waiting.CreditHold != "" {
				t.Fatalf("waiting unit: %+v %v; want %q", view.Waiting, err, test.want)
			}
		})
	}
}

func TestCreditNoticeCountsAnActionsRunner(t *testing.T) {
	f := rankingFixture(t, false)
	if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE capabilities SET revoked_at=1 WHERE subject_kind='machine' AND bind_kind='none'`); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Actions)
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
		t.Fatalf("an eligible Actions runner did not lift the credit hold: %+v %v", view.Waiting, err)
	}
}

func TestRankingAnUnpricedCloudClassDoesNotBlockAnotherClaimant(t *testing.T) {
	f := rankingFixture(t, true)
	f.RequireOnJobs(t, "sparkwing-4cpu,team-4cpu")
	table := store.CreditRateTable{{Cores: 1, MicroPerSecond: 1}}
	if _, err := f.Store.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{RateTable: &table}); err != nil {
		t.Fatal(err)
	}
	if err := f.Rank(t, f.Admin, store.TierCloud, "self-hosted"); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Launcher)
	f.At(time.Second)
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ReadRunView(t.Context(), "run"); err != nil {
		t.Fatalf("an unpriced Cloud class broke the run view: %v", err)
	}
	wantAward(t, f, f.Self, capability.UnitNode, "job")
}

func TestRankingPersonalMachinesTakeOnlyTheirOwnersManualRuns(t *testing.T) {
	for origin, eligible := range map[runspec.Origin]bool{runspec.OriginTrigger: true, runspec.OriginApp: false, runspec.OriginSchedule: false} {
		t.Run(string(origin), func(t *testing.T) {
			f := rankingFixture(t, true)
			f.Request(t, func(r *runspec.Request) { r.Origin = origin })
			livePolls(t, f, f.Personal)
			f.At(time.Second)
			if eligible {
				wantAward(t, f, f.Personal, capability.UnitPlan, "")
				return
			}
			wantNone(t, f, f.Personal, capability.UnitPlan)
			wantAward(t, f, f.Self, capability.UnitPlan, "")
		})
	}
}

func TestRankingNoticeUsesTheClaimLivenessHorizon(t *testing.T) {
	for _, horizon := range []time.Duration{5 * time.Second, 2 * time.Minute} {
		t.Run(horizon.String(), func(t *testing.T) {
			f := rankingFixture(t, true)
			finishRankingTrigger(t, f)
			f.Poll(t, f.Self)
			f.At(30 * time.Second)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Liveness: horizon})
			view, err := tenant.ReadRunView(ctx, "run")
			if err != nil || view.Waiting == nil {
				t.Fatalf("read waiting: %+v %v", view.Waiting, err)
			}
			wantOnline := horizon > 30*time.Second
			if gotOnline := view.Waiting.NoEligible == ""; gotOnline != wantOnline {
				t.Fatalf("horizon %v notice %q; online=%t", horizon, view.Waiting.NoEligible, wantOnline)
			}
		})
	}
}

func TestRankingHoldEndsAtTheWindowWithoutAwardGrace(t *testing.T) {
	f := rankingFixture(t, true)
	f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginApp })
	livePolls(t, f, f.Actions)
	f.At(limits.RankingWindow - time.Nanosecond)
	wantNone(t, f, f.Self, capability.UnitPlan)
	f.At(limits.RankingWindow)
	wantAward(t, f, f.Launcher, capability.UnitPlan, "")
}

func TestRankingAnOfflineBetterTierCausesNoWait(t *testing.T) {
	f := rankingFixture(t, true)
	f.At(time.Second)
	wantAward(t, f, f.Self, capability.UnitPlan, "")
}

func TestRankingABusyBetterTierCausesNoWait(t *testing.T) {
	f := rankingFixture(t, true)
	f.At(-time.Second)
	f.PollWithSlots(t, f.Personal, 0)
	f.At(time.Second)
	wantAward(t, f, f.Self, capability.UnitPlan, "")
}

func TestRankingHoldsOnlyForMatchingLabels(t *testing.T) {
	f := rankingFixture(t, true)
	f.RequireOnJobs(t, "gpu")
	livePolls(t, f, f.Personal)
	f.At(time.Second)
	claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Self, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"gpu"}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("nonmatching better tier held job: %+v %v", claim, err)
	}
}

func TestRankingAnExpiredBetterPollCausesNoWait(t *testing.T) {
	f := rankingFixture(t, true)
	f.At(-time.Second)
	f.Poll(t, f.Personal)
	f.At(time.Second)
	ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Liveness: time.Second})
	claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Self, Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil || claim.Kind != capability.UnitPlan {
		t.Fatalf("expired better poll held plan: %+v %v", claim, err)
	}
}

func TestRankingPlanAwardUsesRemainingPollCapacity(t *testing.T) {
	for name, slots := range map[string]int{"last slot": 1, "spare slot": 2} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			f.At(time.Second)
			f.PollWithSlots(t, f.Self, slots)
			wantNone(t, f, f.Launcher, capability.UnitPlan)
			claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Self, Kinds: []capability.UnitKind{capability.UnitPlan}})
			if err != nil || claim.Kind != capability.UnitPlan {
				t.Fatalf("claim the better tier's plan: %+v %v", claim, err)
			}
			if slots == 1 {
				wantAward(t, f, f.Launcher, capability.UnitNode, "job")
				return
			}
			wantNone(t, f, f.Launcher, capability.UnitNode)
			claim, err = f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Self, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"sparkwing-4cpu"}})
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("claim the job with the remaining slot: %+v %v", claim, err)
			}
		})
	}
}
