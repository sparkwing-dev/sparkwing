package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func rankingFixture(t *testing.T, funded bool) *store.RankingFixture {
	t.Helper()
	return storetest.Open(t).TestOnlyRankingFixture(t, funded)
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

func TestRankingLowerTiersWaitOutTheWindowAndTheBestAskerWins(t *testing.T) {
	f := rankingFixture(t, true)
	livePolls(t, f, f.Actions)
	f.At(time.Second)
	wantNone(t, f, f.Launcher, capability.UnitPlan)
	f.At(2 * time.Second)
	wantNone(t, f, f.Self, capability.UnitPlan)
	f.At(6 * time.Second)
	wantNone(t, f, f.Launcher, capability.UnitPlan)
	f.At(6500 * time.Millisecond)
	wantAward(t, f, f.Self, capability.UnitPlan, "")
}

func TestRankingIsFirstComeAfterTheWindow(t *testing.T) {
	t.Run("nobody better asked in the window", func(t *testing.T) {
		f := rankingFixture(t, true)
		livePolls(t, f, f.Personal, f.Actions)
		f.At(6 * time.Second)
		wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	})
	t.Run("the window winner did not return", func(t *testing.T) {
		f := rankingFixture(t, true)
		livePolls(t, f, f.Actions)
		f.At(time.Second)
		wantNone(t, f, f.Self, capability.UnitPlan)
		f.At(6900 * time.Millisecond)
		wantNone(t, f, f.Launcher, capability.UnitPlan)
		f.At(7 * time.Second)
		wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	})
}

func TestRankingALaterPollKeepsTheWindowAsk(t *testing.T) {
	f := rankingFixture(t, true)
	livePolls(t, f, f.Actions)
	f.At(4900 * time.Millisecond)
	wantNone(t, f, f.Self, capability.UnitPlan)
	f.At(5100 * time.Millisecond)
	f.Poll(t, f.Self, capability.UnitNode)
	f.At(5500 * time.Millisecond)
	wantNone(t, f, f.Launcher, capability.UnitPlan)
	wantAward(t, f, f.Self, capability.UnitPlan, "")
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
			if err := f.Rank(t, f.Admin, store.TierCloud, store.TierSelfHosted); err != nil {
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

func TestRankingAutomatedRunsUseOnlyApprovedPersonalMachinesOfTheAuthor(t *testing.T) {
	app := func(author int64) func(*testing.T, *store.RankingFixture) {
		return func(t *testing.T, f *store.RankingFixture) {
			f.LinkGitHub(t, "owner", 42)
			f.LinkGitHub(t, "member", 43)
			f.Request(t, func(r *runspec.Request) {
				r.Origin = runspec.OriginApp
				r.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "pull_request", AuthorID: author}}
			})
		}
	}
	for name, test := range map[string]struct {
		seal     func(*testing.T, *store.RankingFixture)
		eligible bool
	}{
		"schedule armed by the owner": {seal: func(t *testing.T, f *store.RankingFixture) {
			f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginSchedule })
			if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE runs SET created_subject_kind='service'`); err != nil {
				t.Fatal(err)
			}
		}, eligible: true},
		"schedule armed by an App": {seal: func(t *testing.T, f *store.RankingFixture) {
			f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginSchedule })
			if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE runs SET created_subject_kind='service',created_principal='github-app:7'`); err != nil {
				t.Fatal(err)
			}
		}},
		"pull request authored by the owner":               {seal: app(42), eligible: true},
		"pull request authored by another member":          {seal: app(43)},
		"pull request whose author has no linked identity": {seal: app(44)},
	} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			test.seal(t, f)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			livePolls(t, f, f.Personal)
			f.At(time.Second)
			wantNone(t, f, f.Personal, capability.UnitPlan)
			if _, err := tenant.RequestMachineAutomation(t.Context(), f.Member.Authority, "laptop", true); err == nil {
				t.Fatal("another member requested the owner's machine")
			}
			if state, err := tenant.RequestMachineAutomation(t.Context(), f.Owner.Authority, "laptop", true); err != nil || state != store.AutomationRequested {
				t.Fatalf("request: %s %v", state, err)
			}
			wantNone(t, f, f.Personal, capability.UnitPlan)
			if a, err := f.AdminAuthorization(t, f.Member); err == nil {
				if _, err := tenant.ApproveMachineAutomation(t.Context(), a, "laptop", true); err == nil {
					t.Fatal("an editor approved automated runs")
				}
			}
			f.Approve(t, "laptop")
			wantNone(t, f, f.Personal, capability.UnitPlan)
			if err := tenant.RecordPersonalRepoAccess(t.Context(), f.Personal, map[string]bool{"acme/repo": true}); err != nil {
				t.Fatal(err)
			}
			if !test.eligible {
				wantNone(t, f, f.Personal, capability.UnitPlan)
				return
			}
			wantAward(t, f, f.Personal, capability.UnitPlan, "")
			a, err := f.AdminAuthorization(t, f.Admin)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := tenant.ApproveMachineAutomation(t.Context(), a, "laptop", false); err != nil || state != store.AutomationNone {
				t.Fatalf("revoke: %s %v", state, err)
			}
			wantNone(t, f, f.Personal, capability.UnitNode)
		})
	}
}

func TestRankingPersonalMachinesNeedCheckedRepositoryAccess(t *testing.T) {
	f := rankingFixture(t, true)
	f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginSchedule })
	if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE runs SET created_subject_kind='service'`); err != nil {
		t.Fatal(err)
	}
	f.LinkGitHub(t, "owner", 42)
	f.Approve(t, "laptop")
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	needs, err := tenant.PersonalRepoAccessNeeds(t.Context(), f.Personal)
	if err != nil || len(needs) != 1 || needs[0].Repository != "acme/repo" || needs[0].OwnerGitHubID != 42 {
		t.Fatalf("access needs: %+v %v", needs, err)
	}
	if err := tenant.RecordPersonalRepoAccess(t.Context(), f.Personal, map[string]bool{"acme/repo": false}); err != nil {
		t.Fatal(err)
	}
	f.At(time.Second)
	wantNone(t, f, f.Personal, capability.UnitPlan)
	if err := tenant.RecordPersonalRepoAccess(t.Context(), f.Personal, map[string]bool{"acme/repo": true}); err != nil {
		t.Fatal(err)
	}
	f.At(11 * time.Minute)
	wantNone(t, f, f.Personal, capability.UnitPlan)
	if err := tenant.RecordPersonalRepoAccess(t.Context(), f.Personal, map[string]bool{"acme/repo": true}); err != nil {
		t.Fatal(err)
	}
	wantAward(t, f, f.Personal, capability.UnitPlan, "")
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
	if err := f.Rank(t, f.Admin, store.TierCloud, store.TierSelfHosted, store.TierPersonal); err != nil {
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
		if err := f.Rank(t, f.Admin, store.TierSelfHosted); err != nil {
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

func TestRankingReplacesPreferenceHolds(t *testing.T) {
	f := rankingFixture(t, true)
	if err := f.Rank(t, f.Admin); err != nil {
		t.Fatal(err)
	}
	livePolls(t, f, f.Self)
	f.At(time.Second)
	f.Poll(t, f.Launcher, capability.UnitNode)
	ctx := store.WithClaimPlacement(t.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: []string{"name=runner"}, Hold: time.Hour}, {}}, Liveness: time.Hour})
	claim, err := f.Store.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("first-come ranking still applied a preference hold: %+v %v", claim, err)
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
			if err := f.Rank(t, f.Admin, store.TierHere, store.TierPersonal, store.TierActions, store.TierSelfHosted); err != nil {
				t.Fatal(err)
			}
		}, want: "Cloud is off in this team's runner ranking"},
		"several dropped tiers": {setup: func(t *testing.T, f *store.RankingFixture) {
			if err := f.Rank(t, f.Admin, store.TierSelfHosted); err != nil {
				t.Fatal(err)
			}
		}, want: "Personal machines, GitHub Actions runners and Cloud are off in this team's runner ranking"},
		"a pinned job": {setup: func(t *testing.T, f *store.RankingFixture) {
			f.RequireOnJobs(t, "pool=gpu")
		}, want: "this job targets pool=gpu only"},
		"nobody online":    {setup: func(*testing.T, *store.RankingFixture) {}, want: "no runner of the allowed tiers (here, personal, github-actions, self-hosted, cloud) is online"},
		"an online runner": {setup: func(t *testing.T, f *store.RankingFixture) { f.Poll(t, f.Self) }},
	} {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE triggers SET status='done'`); err != nil {
				t.Fatal(err)
			}
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
	tenant, err := f.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), "run")
	if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
		t.Fatalf("an eligible Actions runner did not lift the credit hold: %+v %v", view.Waiting, err)
	}
}
