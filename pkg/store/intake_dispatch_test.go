package store

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSelectDispatchKeepsSelfHostedSelectorsOnTheHost(t *testing.T) {
	runner := func(kind runspec.RunnerSelectionKind, value string) runspec.Options {
		return runspec.Options{Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: kind, Value: value}}}}
	}
	requires := func(terms ...string) runspec.Options {
		return runspec.Options{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: terms}}}
	}
	for name, test := range map[string]struct {
		cloud    bool
		mode     runspec.IntakeMode
		dispatch runspec.Dispatch
		options  runspec.Options
		want     runspec.Dispatch
	}{
		"local keeps host":             {mode: runspec.IntakeExecute, dispatch: runspec.DispatchHost, want: runspec.DispatchHost},
		"local keeps controller":       {mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, want: runspec.DispatchController},
		"cloud unselected":             {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchHost, want: runspec.DispatchController},
		"cloud any":                    {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchHost, options: runner(runspec.RunnerAny, ""), want: runspec.DispatchController},
		"cloud non-pool requirement":   {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchHost, options: requires("linux"), want: runspec.DispatchController},
		"cloud machine name":           {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, options: runner(runspec.RunnerName, "builder"), want: runspec.DispatchHost},
		"cloud label":                  {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, options: runner(runspec.RunnerLabel, "gpu"), want: runspec.DispatchHost},
		"cloud here":                   {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, options: runner(runspec.RunnerHere, ""), want: runspec.DispatchHost},
		"cloud pool requirement":       {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, options: requires("linux", "pool=prod"), want: runspec.DispatchHost},
		"cloud pool alternative":       {cloud: true, mode: runspec.IntakeExecute, dispatch: runspec.DispatchController, options: requires("arm64, pool=prod"), want: runspec.DispatchHost},
		"cloud publication has no run": {cloud: true, mode: runspec.IntakePublish, dispatch: runspec.DispatchController, want: runspec.DispatchHost},
	} {
		t.Run(name, func(t *testing.T) {
			got := SelectDispatch(test.cloud, runspec.Request{Mode: test.mode, Dispatch: test.dispatch, Options: test.options})
			if got != test.want {
				t.Fatalf("dispatch=%s, want %s", got, test.want)
			}
		})
	}
}

func TestCloudAdmissionSealsDispatchAfterRouteRules(t *testing.T) {
	for name, route := range map[string]bool{"launcher": false, "routed pool": true} {
		t.Run(name, func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			if route {
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,route,created_by,created_at) VALUES ('a','route','prod','gh:1','true','owner',1)`)
			}
			tenant.s.SetIntakePolicy(fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
				return IntakePolicyFacts{RepositoryAuthorized: true, CloudDispatch: true, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
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
