package store

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSelectDispatchRunsUnselectedWorkAnywhereOnEveryController(t *testing.T) {
	runner := func(kind runspec.RunnerSelectionKind, value string) runspec.Options {
		return runspec.Options{Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: kind, Value: value}}}}
	}
	requires := func(terms ...string) runspec.Options {
		return runspec.Options{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: terms}}}
	}
	for name, test := range map[string]struct {
		mode    runspec.IntakeMode
		origin  runspec.Origin
		options runspec.Options
		want    runspec.Dispatch
	}{
		"unselected trigger":       {mode: runspec.IntakeExecute, origin: runspec.OriginTrigger, want: runspec.DispatchController},
		"any runner":               {mode: runspec.IntakeExecute, options: runner(runspec.RunnerAny, ""), want: runspec.DispatchController},
		"non-pool requirement":     {mode: runspec.IntakeExecute, options: requires("linux"), want: runspec.DispatchController},
		"local run keeps its host": {mode: runspec.IntakeExecute, origin: runspec.OriginLocal, want: runspec.DispatchHost},
		"machine name":             {mode: runspec.IntakeExecute, options: runner(runspec.RunnerName, "builder"), want: runspec.DispatchHost},
		"label":                    {mode: runspec.IntakeExecute, options: runner(runspec.RunnerLabel, "gpu"), want: runspec.DispatchHost},
		"here":                     {mode: runspec.IntakeExecute, options: runner(runspec.RunnerHere, ""), want: runspec.DispatchHost},
		"pool requirement":         {mode: runspec.IntakeExecute, options: requires("linux", "pool=prod"), want: runspec.DispatchHost},
		"pool alternative":         {mode: runspec.IntakeExecute, options: requires("arm64, pool=prod"), want: runspec.DispatchHost},
		"publication has no run":   {mode: runspec.IntakePublish, want: runspec.DispatchHost},
	} {
		t.Run(name, func(t *testing.T) {
			got := SelectDispatch(runspec.Request{Mode: test.mode, Origin: test.origin, Dispatch: runspec.DispatchHost, Options: test.options})
			if got != test.want {
				t.Fatalf("dispatch=%s, want %s", got, test.want)
			}
		})
	}
}

func TestAdmissionSealsDispatchAfterRouteRules(t *testing.T) {
	for name, route := range map[string]bool{"launcher": false, "routed pool": true} {
		t.Run(name, func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			request.Request.Dispatch = ""
			if route {
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,route,created_by,created_at) VALUES ('a','route','prod','gh:1','true','owner',1)`)
			}
			tenant.s.SetIntakePolicy(fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
				return IntakePolicyFacts{RepositoryAuthorized: true, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
			}))
			result, err := tenant.AdmitRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			want, kind := runspec.DispatchController, capability.UnitPlan
			if route {
				want, kind = runspec.DispatchHost, capability.UnitTrigger
			}
			var dispatch, unit string
			if err := tenant.s.db.QueryRowContext(t.Context(), `SELECT r.dispatch,t.unit_kind FROM runs r JOIN triggers t ON t.team=r.team AND t.id=r.id WHERE r.id=?`, result.RunID).Scan(&dispatch, &unit); err != nil {
				t.Fatal(err)
			}
			if result.Request.Request.Dispatch != want || dispatch != string(want) || unit != string(kind) {
				t.Fatalf("sealed=%s row=%s unit=%s, want %s %s", result.Request.Request.Dispatch, dispatch, unit, want, kind)
			}
		})
	}
}

func TestPublicIntakeRefusesTheActionsOwner(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}}
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("a create-run request selected the GitHub Actions owner")
	}
}
