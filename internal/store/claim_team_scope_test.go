package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

var launcherKinds = []capability.UnitKind{capability.UnitPlan, capability.UnitNode}

// hack: staging ran 48 live teams when the per-team cost was measured.
const idleTeams = 45

func TestCloudLauncherSearchReadsOnlyTeamsWithQueuedWork(t *testing.T) {
	for _, queued := range []bool{true, false} {
		name := "work queued in one team"
		if !queued {
			name = "no work queued"
		}
		t.Run(name, func(t *testing.T) {
			claim := func(idle int) store.StatementCount {
				t.Helper()
				f := rankingFixture(t, true)
				if !queued {
					f.ClearQueue(t)
				}
				f.AddIdleTeams(t, idle)
				count := f.Store.TestOnlyCountStatements(t)
				f.At(time.Minute)
				f.Poll(t, f.Launcher, launcherKinds...)
				count.Take()
				award, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: launcherKinds, Labels: []string{"team-4cpu"}})
				if queued && err != nil || !queued && !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("launcher claim with %d idle teams: %+v %v", idle, award, err)
				}
				return count.Take()
			}
			few, many := claim(0), claim(idleTeams)
			t.Logf("launcher claim with no idle teams %+v, beside %d idle teams %+v", few, idleTeams, many)
			if many != few {
				t.Fatalf("the launcher's claim grew with idle teams: %+v, then %+v", few, many)
			}
			if !queued && (many.StandingReads != 0 || many.PaymentModeReads != 0) {
				t.Fatalf("an empty launcher search built team facts: %+v", many)
			}
		})
	}
}

func TestClaimTeamFactsReadThePaymentModeOncePerSearch(t *testing.T) {
	f := rankingFixture(t, true)
	f.AddIdleTeams(t, idleTeams)
	count := f.Store.TestOnlyCountStatements(t)
	teams := f.ClaimTeams(t)
	got := count.Take()
	if len(teams) < idleTeams || got.StandingReads != len(teams) || got.PaymentModeReads != 1 {
		t.Fatalf("facts for %d teams read %d standings and the payment mode %d times", len(teams), got.StandingReads, got.PaymentModeReads)
	}
}

// Every unit a Cloud launcher can be awarded must leave its team in the launcher's search, whatever the work's
// kind, placement or funding: a team missing from it reads as an empty queue until the next wake.
func TestCloudLauncherSearchKeepsEveryTeamWithClaimableWork(t *testing.T) {
	type scenario struct {
		kind  capability.UnitKind
		job   string
		setup func(*testing.T, *store.RankingFixture) time.Duration
	}
	scenarios := map[string]scenario{
		"plan unit": {kind: capability.UnitPlan, setup: func(*testing.T, *store.RankingFixture) time.Duration { return time.Minute }},
		"job": {kind: capability.UnitNode, job: "job", setup: func(t *testing.T, f *store.RankingFixture) time.Duration {
			finishRankingTrigger(t, f)
			return time.Minute
		}},
		"job requeued after a lapsed award": {kind: capability.UnitNode, job: "job", setup: func(t *testing.T, f *store.RankingFixture) time.Duration {
			finishRankingTrigger(t, f)
			f.At(time.Minute)
			first, err := f.Claim(t, f.Self, capability.UnitNode)
			if err != nil {
				t.Fatal(err)
			}
			lapsed := first.Award.LeaseExpiresAt.Sub(f.ReadyAt) + time.Second
			f.At(lapsed)
			reaped, err := f.Store.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("reaping the lapsed award: %+v %v", reaped, err)
			}
			if teams := f.ClaimTeams(t, launcherKinds...); len(teams) != 0 {
				t.Fatalf("a requeued job waits for its plan to ready it again, yet the launcher would search %v", teams)
			}
			f.ReadyJobAgain(t, "job")
			return lapsed
		}},
		"job held for a better-ranked runner": {kind: capability.UnitNode, job: "job", setup: func(t *testing.T, f *store.RankingFixture) time.Duration {
			finishRankingTrigger(t, f)
			f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginApp })
			livePolls(t, f, f.SelfHosted(t, "builder"))
			f.At(time.Second)
			if teams := f.ClaimTeams(t, launcherKinds...); !slices.Equal(teams, []string{"a"}) {
				t.Fatalf("the launcher's search dropped held work: %v", teams)
			}
			wantNone(t, f, f.Launcher, capability.UnitNode)
			return limits.RankingWindow + time.Second
		}},
	}
	for name, test := range scenarios {
		for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
			for _, funded := range []bool{true, false} {
				funding := "free credits"
				if !funded {
					funding = "no credits"
				}
				t.Run(name+"/"+string(mode)+"/"+funding, func(t *testing.T) {
					f := rankingFixture(t, false)
					f.SetPaymentMode(t, mode)
					if funded {
						f.Fund(t)
					}
					f.AddIdleTeams(t, idleTeams)
					f.At(test.setup(t, f))
					if teams := f.ClaimTeams(t, launcherKinds...); !slices.Equal(teams, []string{"a"}) {
						t.Fatalf("the launcher would search %v, want only the team holding work", teams)
					}
					if !funded {
						wantNone(t, f, f.Launcher, test.kind)
						return
					}
					wantAward(t, f, f.Launcher, test.kind, test.job)
				})
			}
		}
	}
}

func TestLauncherExistenceCheckUsesThePartialClaimIndexesOnPostgres(t *testing.T) {
	plan := store.LauncherClaimTeamsPlan(t, storetest.OpenPostgres(t))
	t.Log(plan)
	for _, index := range []string{"using idx_nodes_claim_", "using idx_eval_units_claimable ", "using idx_triggers_claim_"} {
		if !strings.Contains(plan, index) {
			t.Fatalf("the plan does not use a partial claim index (%s):\n%s", strings.TrimPrefix(index, "using "), plan)
		}
	}
}
