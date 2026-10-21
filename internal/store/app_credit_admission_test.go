package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestUnfundedPushIsHistoryAndNeverStartsAfterFunding(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "unfunded")
	defer s.Close()
	in.Request.Dispatch = runspec.DispatchController
	result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), result.RunID)
	if err != nil || view.Snapshot.Run.State != RunFailed || view.Snapshot.Run.StartedAt != nil || view.Snapshot.Run.FinishedAt == nil || (len(view.Failures) != 1 || !strings.HasPrefix(view.Failures[0], "run did not start: out of credits:")) || result.OwnerUnit != nil {
		t.Fatalf("unfunded push must be terminal history: result=%+v view=%+v err=%v", result, view, err)
	}
	rows, err := tenant.ListRuns(t.Context(), RunFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != result.RunID || rows[0].Status != "failed" || rows[0].Error != view.Failures[0] {
		t.Fatalf("run list lost unfunded push: %+v, %v", rows, err)
	}
	rewriteSchemaCount(t, s.db, "events WHERE kind='run_failed'", 1)
	rewriteSchemaCount(t, s.db, "triggers WHERE status='pending'", 0)
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
	replay, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil || !replay.Replayed || replay.RunID != result.RunID {
		t.Fatalf("redelivery after funding changed history: %+v, %v", replay, err)
	}
	launcher := authorityRoot("launcher", "")
	launcher.Subject.Kind = capability.SubjectService
	launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, launcher)
	if _, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: launcher.Prefix, Kinds: []capability.UnitKind{capability.UnitPlan}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("funding started the old push: %v", err)
	}
	member := authorityRoot("rerun", "a")
	member.Grants = []capability.Grant{capability.RunsWrite, capability.RunsControl}
	authorityPersist(t, s, member)
	s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})
	rerunRequest := IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: member.Prefix}, Request: runspec.Request{Pipeline: "ci", Lineage: runspec.Lineage{RetryOf: result.RunID}}, RetryMode: "all"}
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	if _, err := tenant.AdmitRun(t.Context(), rerunRequest); err == nil || !strings.Contains(err.Error(), "out of credits:") {
		t.Fatalf("unfunded run-now must refuse without queuing: %v", err)
	}
	rewriteSchemaCount(t, s.db, "runs", 1)
	rewriteSchemaCount(t, s.db, "triggers WHERE status='pending'", 0)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
	rerun, err := tenant.AdmitRun(t.Context(), rerunRequest)
	if err != nil || rerun.RunID == result.RunID {
		t.Fatalf("funded run-now could not retry an unplanned run: %+v, %v", rerun, err)
	}
	view, err = tenant.ReadRunView(t.Context(), rerun.RunID)
	if err != nil || view.Snapshot.Run.State != RunPending {
		t.Fatalf("run-now did not create pending work: %+v, %v", view, err)
	}
}

func TestUnfundedPushCanStillUseAnEligibleUnmeteredRunner(t *testing.T) {
	s, tenant, in := appAdmissionFixture(t, true, "unmetered")
	defer s.Close()
	in.Request.Dispatch = runspec.DispatchController
	for _, id := range []string{"launcher", "runner"} {
		root := authorityRoot(id, "a")
		root.Grants = []capability.Grant{capability.TriggersClaim}
		if id == "launcher" {
			root.Team, root.Subject.Kind = "", capability.SubjectService
			root.Grants = []capability.Grant{capability.ClaimsLaunch}
		}
		authorityPersist(t, s, root)
	}
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: "runner", Kinds: []capability.UnitKind{capability.UnitPlan}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}); err != nil {
		t.Fatal(err)
	}
	result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), result.RunID)
	if err != nil || run.State != RunPending || result.OwnerUnit == nil {
		t.Fatalf("unmetered push was blocked by credits: state=%s owner=%+v err=%v", run.State, result.OwnerUnit, err)
	}
}

func TestPushCreditAdmissionUsesSealedPlacementWithoutLaunchers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requires []string
		ranking  string
		failed   bool
	}{
		{name: "Cloud required", requires: []string{"class=cloud"}, failed: true},
		{name: "Cloud labels before registration", requires: []string{"linux", "arch=arm64"}, failed: true},
		{name: "mixed class or label can use Cloud", requires: []string{"class=agent,gpu"}, failed: true},
		{name: "self hosted required", requires: []string{"class=agent"}},
		{name: "pool required", requires: []string{"pool=private"}},
		{name: "Cloud excluded from ranking", ranking: `["self-hosted"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tenant, in := appAdmissionFixture(t, true, "placement")
			defer s.Close()
			in.Request.Dispatch = runspec.DispatchController
			in.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: tc.requires}}
			if tc.ranking != "" {
				rewriteSchemaExec(t, s.db, `UPDATE teams SET claim_ranking=? WHERE name='a'`, tc.ranking)
			}
			result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := RunPending
			if tc.failed {
				want = RunFailed
			}
			if run.State != want || (result.OwnerUnit == nil) != tc.failed {
				t.Fatalf("state=%s owner=%+v want %s", run.State, result.OwnerUnit, want)
			}
		})
	}
}
