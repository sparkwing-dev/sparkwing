package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestUnfundedPushIsHistoryAndNeverStartsAfterFunding(t *testing.T) {
	for _, requirement := range []string{"class=cloud", "sparkwing-16cpu"} {
		t.Run(requirement, func(t *testing.T) {
			testUnfundedPushIsHistoryAndNeverStartsAfterFunding(t, requirement)
		})
	}
}

func testUnfundedPushIsHistoryAndNeverStartsAfterFunding(t *testing.T, requirement string) {
	t.Helper()
	s, tenant, in := appAdmissionFixture(t, true, "unfunded")
	defer s.Close()
	rates := CreditRateTable{{Cores: 2, MicroPerSecond: 2}, {Cores: 16, MicroPerSecond: 16}}
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
		t.Fatal(err)
	}
	in.Request.Dispatch = runspec.DispatchController
	in.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{requirement}}}
	result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := tenant.ReadRunView(t.Context(), result.RunID)
	if err != nil || view.Snapshot.Run.State != runstate.RunFailed || view.Snapshot.Run.StartedAt != nil || view.Snapshot.Run.FinishedAt == nil || (len(view.Failures) != 1 || !strings.HasPrefix(view.Failures[0].Error, "run did not start: out of credits:")) || result.OwnerUnit != nil {
		t.Fatalf("unfunded push must be terminal history: result=%+v view=%+v err=%v", result, view, err)
	}
	rows, err := tenant.ListRuns(t.Context(), RunFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != result.RunID || rows[0].Status != "failed" || rows[0].Error != view.Failures[0].Error {
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
	if err != nil || view.Snapshot.Run.State != runstate.RunPending {
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
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: "runner", Kinds: []capability.UnitKind{capability.UnitPlan}}); err != nil {
		t.Fatal(err)
	}
	result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), result.RunID)
	if err != nil || run.State != runstate.RunPending || result.OwnerUnit == nil {
		t.Fatalf("unmetered push was blocked by credits: state=%s owner=%+v err=%v", run.State, result.OwnerUnit, err)
	}
}

func TestPushCreditAdmissionUsesSealedPlacementWithoutLaunchers(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requires    []string
		ranking     string
		runnerLabel string
		failed      bool
		funded      bool
		hold        bool
		revoked     bool
	}{
		{name: "default permits free"},
		{name: "funded Cloud", requires: []string{"class=cloud"}, funded: true},
		{name: "held Cloud cannot use positive balance", requires: []string{"class=cloud"}, funded: true, hold: true, failed: true},
		{name: "revoked Cloud cannot use positive balance", requires: []string{"class=cloud"}, funded: true, revoked: true, failed: true},
		{name: "exclusive Cloud billing policy", ranking: `["cloud"]`, failed: true},
		{name: "empty ranking permits free", ranking: `[]`},
		{name: "Cloud required", requires: []string{"class=cloud"}, failed: true},
		{name: "Cloud size required", requires: []string{"sparkwing-16cpu"}, failed: true},
		{name: "funded Cloud size", requires: []string{"sparkwing-16cpu"}, funded: true},
		{name: "Cloud size and ordinary label", requires: []string{"sparkwing-16cpu", "gpu"}, failed: true},
		{name: "Cloud size alternatives", requires: []string{"sparkwing-16cpu,sparkwing-32cpu"}, failed: true},
		{name: "Cloud size or free label", requires: []string{"sparkwing-16cpu,gpu"}},
		{name: "Cloud size or free class", requires: []string{"sparkwing-16cpu,class=agent"}},
		{name: "Cloud size excludes free alternative", requires: []string{"sparkwing-16cpu,class=agent", "class=cloud"}, failed: true},
		{name: "conflicting Cloud sizes", requires: []string{"sparkwing-16cpu", "sparkwing-32cpu"}},
		{name: "runner label requires Cloud size", runnerLabel: "sparkwing-16cpu", failed: true},
		{name: "runner label requires Cloud", runnerLabel: "class=cloud", failed: true},
		{name: "runner label permits free", runnerLabel: "gpu"},
		{name: "free labels before registration", requires: []string{"linux", "arch=arm64"}},
		{name: "mixed class or label permits free", requires: []string{"class=agent,gpu"}},
		{name: "self hosted required", requires: []string{"class=agent"}},
		{name: "pool required", requires: []string{"pool=private"}},
		{name: "Cloud excluded from ranking", ranking: `["self-hosted"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tenant, in := appAdmissionFixture(t, true, "placement")
			defer s.Close()
			rates := CreditRateTable{{Cores: 2, MicroPerSecond: 2}, {Cores: 16, MicroPerSecond: 16}, {Cores: 32, MicroPerSecond: 32}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			in.Request.Dispatch = runspec.DispatchController
			in.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: tc.requires}}
			if tc.runnerLabel != "" {
				in.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: tc.runnerLabel}}}
			}
			if tc.ranking != "" {
				rewriteSchemaExec(t, s.db, `UPDATE teams SET claim_ranking=? WHERE name='a'`, tc.ranking)
			}
			if tc.funded {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('fund','a','paid',1000000,1)`)
			}
			if tc.hold {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,created_at,cause,placed_by) VALUES ('hold','a',1,'operator','operator')`)
			}
			if tc.revoked {
				rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='revoked' WHERE name='a'`)
			}
			result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := runstate.RunPending
			if tc.failed {
				want = runstate.RunFailed
			}
			if run.State != want || (result.OwnerUnit == nil) != tc.failed {
				t.Fatalf("state=%s owner=%+v want %s", run.State, result.OwnerUnit, want)
			}
		})
	}
}
