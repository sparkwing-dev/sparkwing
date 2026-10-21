package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func meteredRunnerOnly(t *testing.T, s *Store) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='runner'`)
	paid := authorityRoot("paid-runner", "a")
	paid.RootMetered, paid.Grants = true, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, s, paid)
}

func executingRunFixture(t *testing.T, dispatch runspec.Dispatch) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	request := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Mode: runspec.IntakeExecute, Dispatch: dispatch, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,dispatch=?`, request, dispatch)
	return s, demand
}

func TestUnclaimedLauncherWorkNamesTheCreditHold(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand := executingRunFixture(t, runspec.DispatchController)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			meteredRunnerOnly(t, s)
			if kind == capability.UnitNode {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='done',finished_at=?`, s.now().UnixNano())
			} else {
				rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			}
			root := authorityRoot("launcher", "")
			root.Subject.Kind = capability.SubjectService
			root.Grants = []capability.Grant{capability.ClaimsLaunch}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix, demand.Target, demand.Kinds = root.Prefix, nil, []capability.UnitKind{kind}
			tenant := &Tenant{s: s, team: "a"}
			view, err := tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil || view.Waiting.Kind != kind || !strings.HasPrefix(view.Waiting.CreditHold, "out of credits:") || !strings.Contains(view.Waiting.CreditHold, "add funds") {
				t.Fatalf("unfunded waiting unit = %+v, %v", view.Waiting, err)
			}
			if _, err = s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("launcher claimed unfunded work: %v", err)
			}
			fundUnitClaim(t, s)
			view, err = tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
				t.Fatalf("funded waiting unit = %+v, %v", view.Waiting, err)
			}
			if _, err = s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
				t.Fatalf("launcher refused funded work: %v", err)
			}
		})
	}
}

func TestUnmeteredRunnerWorkCarriesNoCreditHold(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		for _, runner := range []string{"unmetered", "metered", "revoked"} {
			t.Run(string(kind)+"/"+runner, func(t *testing.T) {
				unmeteredRunnerCase(t, kind, runner)
			})
		}
	}
}

func unmeteredRunnerCase(t *testing.T, kind capability.UnitKind, runner string) {
	t.Helper()
	s, demand := executingRunFixture(t, runspec.DispatchController)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	if kind == capability.UnitPlan {
		rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
	} else {
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='done',finished_at=?`, s.now().UnixNano())
	}
	switch runner {
	case "metered":
		meteredRunnerOnly(t, s)
		if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: "paid-runner", Labels: []string{"sparkwing-4cpu"}, Kinds: []capability.UnitKind{kind}}); err != nil {
			t.Fatal(err)
		}
	case "revoked":
		rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='runner'`)
	}
	view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
	if err != nil || view.Waiting == nil || view.Waiting.Kind != kind {
		t.Fatalf("waiting unit = %+v, %v", view.Waiting, err)
	}
	if want := map[string]bool{"unmetered": false, "metered": true, "revoked": false}[runner]; strings.HasPrefix(view.Waiting.CreditHold, "out of credits:") != want {
		t.Fatalf("%s runner: credit hold = %q, want an out-of-credits hold %v", runner, view.Waiting.CreditHold, want)
	}
	if runner != "unmetered" {
		return
	}
	demand.Target, demand.Kinds = nil, []capability.UnitKind{kind}
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
		t.Fatalf("the unmetered runner the notice relied on could not claim the %s: %v", kind, err)
	}
}

func TestUnclaimedRunnerWorkCarriesNoCreditHold(t *testing.T) {
	s, _ := executingRunFixture(t, runspec.DispatchHost)
	view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
	if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
		t.Fatalf("runner-hosted waiting unit = %+v, %v", view.Waiting, err)
	}
}
