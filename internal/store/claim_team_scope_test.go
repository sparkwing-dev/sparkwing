package store_test

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

var launcherKinds = []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}

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
				f.PollIdleRunners(t)
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

func TestCloudLauncherSearchReadsThePaymentModeOnce(t *testing.T) {
	f := rankingFixture(t, true)
	overflow := int64(600)
	f.Request(t, func(r *runspec.Request) {
		r.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}, OverflowMinutes: &overflow}
	})
	finishRankingTrigger(t, f)
	f.AddIdleTeams(t, idleTeams)
	count := f.Store.TestOnlyCountStatements(t)
	f.At(time.Minute)
	f.PollIdleRunners(t)
	f.Poll(t, f.Launcher, launcherKinds...)
	count.Take()
	if err := f.SearchOnce(t, launcherKinds...); err != nil {
		t.Fatal(err)
	}
	if got := count.Take(); got.PaymentModeReads != 1 || got.StandingReads != 1 {
		t.Fatalf("one launcher search with overflow funding read %d standings and the payment mode %d times", got.StandingReads, got.PaymentModeReads)
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
		"eval unit alone": {kind: capability.UnitEval, job: "job", setup: func(t *testing.T, f *store.RankingFixture) time.Duration {
			f.QueueEvalOnly(t)
			return time.Minute
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

// The launcher's team list must cost the same whatever one team's backlog holds: a seek per team with work.
func TestLauncherTeamListStaysBoundedOnASkewedBacklogOnPostgres(t *testing.T) {
	s := storetest.OpenPostgres(t)
	store.SeedSkewedBacklog(t, s, 100000)
	plan, teams := store.LauncherClaimTeamsPlan(t, s)
	t.Log(plan)
	if want := []string{"skew-00", "skew-01", "skew-02", "skew-03"}; !slices.Equal(teams, want) {
		t.Fatalf("the launcher would search %v, want %v", teams, want)
	}
	for _, scan := range []string{"Seq Scan on nodes", "Seq Scan on triggers", "Seq Scan on eval_units", "Bitmap Heap Scan", "hashed SubPlan"} {
		if strings.Contains(plan, scan) {
			t.Fatalf("the team list reads a backlog (%s):\n%s", scan, plan)
		}
	}
	match := regexp.MustCompile(`Buffers: shared hit=(\d+)(?: read=(\d+))?`).FindStringSubmatch(plan)
	if match == nil {
		t.Fatalf("no buffer count in the plan:\n%s", plan)
	}
	pages := 0
	for _, count := range match[1:] {
		if n, err := strconv.Atoi(count); err == nil {
			pages += n
		}
	}
	if pages > 500 {
		t.Fatalf("the team list touched %d pages over a 100,000-unit backlog; a seek per team needs far fewer:\n%s", pages, plan)
	}
}
