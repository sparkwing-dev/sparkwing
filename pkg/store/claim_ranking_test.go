package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
		f.At(9 * time.Second)
		wantNone(t, f, f.Launcher, capability.UnitPlan)
		f.At(10 * time.Second)
		wantAward(t, f, f.Launcher, capability.UnitPlan, "")
	})
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

func TestRankingAutomatedRunsSkipPersonalMachinesUntilTheOwnerOptsIn(t *testing.T) {
	automated := map[string]func(*testing.T, *store.RankingFixture){
		"schedule": func(t *testing.T, f *store.RankingFixture) {
			f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginSchedule })
			if _, err := f.Store.DB().ExecContext(t.Context(), `UPDATE runs SET created_subject_kind='service'`); err != nil {
				t.Fatal(err)
			}
		},
		"push": func(t *testing.T, f *store.RankingFixture) {
			f.LinkGitHub(t, "owner", 42)
			f.Request(t, func(r *runspec.Request) {
				r.Origin = runspec.OriginApp
				r.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", SenderID: 42}}
			})
		},
	}
	for name, seal := range automated {
		t.Run(name, func(t *testing.T) {
			f := rankingFixture(t, true)
			seal(t, f)
			tenant, err := f.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			livePolls(t, f, f.Personal)
			f.At(time.Second)
			wantNone(t, f, f.Personal, capability.UnitPlan)
			if err := tenant.SetMachineAutomation(t.Context(), f.Member.Authority, "laptop", true); err == nil {
				t.Fatal("another member opted the owner's machine in")
			}
			if err := tenant.SetMachineAutomation(t.Context(), f.Owner.Authority, "desktop", true); err == nil {
				t.Fatal("the owner opted another member's machine in")
			}
			if err := tenant.SetMachineAutomation(t.Context(), f.Owner.Authority, "laptop", true); err != nil {
				t.Fatal(err)
			}
			wantNone(t, f, f.Self, capability.UnitPlan)
			wantNone(t, f, f.Second, capability.UnitPlan)
			wantAward(t, f, f.Personal, capability.UnitPlan, "")
			if err := tenant.SetMachineAutomation(t.Context(), f.Owner.Authority, "laptop", false); err != nil {
				t.Fatal(err)
			}
			wantNone(t, f, f.Personal, capability.UnitNode)
			wantAward(t, f, f.Self, capability.UnitNode, "job")
		})
	}
	t.Run("an unlinked sender owns nothing", func(t *testing.T) {
		f := rankingFixture(t, true)
		f.Request(t, func(r *runspec.Request) {
			r.Origin = runspec.OriginApp
			r.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", SenderID: 43}}
		})
		tenant, err := f.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		if err := tenant.SetMachineAutomation(t.Context(), f.Owner.Authority, "laptop", true); err != nil {
			t.Fatal(err)
		}
		f.At(time.Second)
		wantNone(t, f, f.Personal, capability.UnitPlan)
		wantAward(t, f, f.Self, capability.UnitPlan, "")
	})
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
