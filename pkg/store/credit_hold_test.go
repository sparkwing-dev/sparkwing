package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func executingRunFixture(t *testing.T) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	request, err := json.Marshal(runspec.SealedRequest{Request: runspec.Request{Mode: runspec.IntakeExecute, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?`, request)
	return s, demand
}

func TestUnclaimedLauncherWorkNamesTheCreditHold(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand := executingRunFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			if kind == capability.UnitNode {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='done'`)
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
			if err != nil || view.Waiting == nil || view.Waiting.Kind != kind || view.Waiting.CreditHold != NoCreditsHold {
				t.Fatalf("unfunded waiting unit = %+v, %v", view.Waiting, err)
			}
			if _, err = s.ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("launcher claimed unfunded work: %v", err)
			}
			fundUnitClaim(t, s)
			view, err = tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
				t.Fatalf("funded waiting unit = %+v, %v", view.Waiting, err)
			}
			if _, err = s.ClaimUnit(t.Context(), demand); err != nil {
				t.Fatalf("launcher refused funded work: %v", err)
			}
		})
	}
}

func TestUnclaimedRunnerWorkCarriesNoCreditHold(t *testing.T) {
	s, _ := executingRunFixture(t)
	view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
	if err != nil || view.Waiting == nil || view.Waiting.CreditHold != "" {
		t.Fatalf("runner-hosted waiting unit = %+v, %v", view.Waiting, err)
	}
}
