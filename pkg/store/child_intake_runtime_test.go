package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func childIntakeFixture(t *testing.T) (*Tenant, IntakeRequest) {
	t.Helper()
	tenant, _, actor := liveHolderFixture(t)
	parent := authorityRoot("child-parent", "a")
	parent.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: actor.Holder, Generation: actor.Generation}
	parent.Grants = []capability.Grant{capability.ChildrenStart}
	parent.Children = []nodeproto.ChildPipeline{{Pipeline: "child", Repository: "gh:1"}}
	authorityPersist(t, tenant.s, parent)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET capability_prefix='child-parent'`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"id":"job","children":[{"pipeline":"child"}]}`))
	// Intake seals the parent's repository alongside its repo_key column.
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, sealWith(t, runspec.SealedRequest{Version: 1, Request: runspec.Request{Pipeline: "ci", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}}}, assertedProvenance(t, "ci", "", "")))
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET lineage_json=? WHERE id='run'`, []byte(`{"admission_key":"capability:root","admission_subject":{"kind":"account","id":"alice"}}`))
	tenant.s.intake = fakeIntakeContracts{}
	tenant.s.SetIntakePolicy(fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	}))
	return tenant, IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}, IdempotencyKey: "invocation", Request: runspec.Request{Version: 1, Team: "a", Pipeline: "child", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}}}
}

func TestChildIntakePersistsLineageAndInvocationReplay(t *testing.T) {
	tenant, request := childIntakeFixture(t)
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || first.Replayed || first.OwnerUnit == nil || first.Request.Request.Origin != runspec.OriginChild || first.Request.Request.Lineage != (runspec.Lineage{ParentRunID: "run", ParentJobID: "job", RootRunID: "run", AdmissionSubject: capability.Subject{Kind: capability.SubjectAccount, ID: "alice"}}) {
		t.Fatalf("child admission %q replayed=%t: %v", first.RunID, first.Replayed, err)
	}
	for range 2 {
		replayed, err := tenant.AdmitRun(t.Context(), request)
		if err != nil || !replayed.Replayed || replayed.RunID != first.RunID {
			t.Fatalf("child replay: %+v, %v", replayed, err)
		}
	}
	request.Request.RequestedArgs = map[string]string{"different": "body"}
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("changed child request reused an invocation key")
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 2)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=?`, tenant.s.now().UnixNano())
	request.Request.RequestedArgs = nil
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("expired parent replayed child intake")
	}
}

func TestChildIntakeCannotEscapeItsLiveParentProperty(t *testing.T) {
	for i := range 24 {
		for _, dimension := range []string{"run", "job", "team", "generation", "cancelled", "finished"} {
			tenant, request := childIntakeFixture(t)
			value := fmt.Sprintf("foreign-%d", i)
			switch dimension {
			case "run":
				request.Request.Lineage.ParentRunID = value
			case "job":
				request.Request.Lineage.ParentJobID = value
			case "team":
				request.Request.Team = value
			case "generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=?`, 3+i)
			case "cancelled":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1`)
			case "finished":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix=''`)
			}
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
				t.Fatalf("child escaped %s fence", dimension)
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 1)
			rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
		}
	}
}

func TestChildIntakeInheritsTheActionsOwnerOnlyFromItsParent(t *testing.T) {
	for _, parentActions := range []bool{false, true} {
		tenant, request := childIntakeFixture(t)
		if parentActions {
			row, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			row.Request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}}
			body, err := json.Marshal(row.Request)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
		}
		request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}}
		_, err := tenant.AdmitRun(t.Context(), request)
		if parentActions != (err == nil) {
			t.Fatalf("parent sealed with Actions=%t: child admission error %v", parentActions, err)
		}
	}
}

func TestIntakeRefusesAClientDeclaredScheduleOrigin(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.Request.Origin = runspec.OriginSchedule
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil || !strings.Contains(err.Error(), "schedule firing") {
		t.Fatalf("a client started a schedule run: %v", err)
	}
}

func TestChildIntakeUsesSealedTargetsAfterPlanMutation(t *testing.T) {
	tenant, request := childIntakeFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"id":"job","children":[{"pipeline":"foreign","repository":"gh:2"}]}`))
	request.Request.Pipeline = "foreign"
	request.Request.Git.Repository.Key = "gh:2"
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("mutated plan granted a child target")
	}
	request.Request.Pipeline = "child"
	request.Request.Git.Repository.Key = "gh:1"
	if _, err := tenant.AdmitRun(t.Context(), request); err != nil {
		t.Fatalf("sealed target lost authority after plan mutation: %v", err)
	}
}

func TestChildOfRetriedChildSpendsRetriersHourlyBudget(t *testing.T) {
	tenant, request := childIntakeFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_subject_kind,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) SELECT team,'original-root',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,'account','alice',created_at,?,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, []byte(`{"admission_subject":{"kind":"account","id":"alice"}}`))
	rewriteSchemaExec(t, s.db, `UPDATE runs SET parent_run_id='original-root',retry_of='previous-child',created_subject_kind='account',created_principal='bob',lineage_json=? WHERE team='a' AND id='run'`, []byte(`{"root_run_id":"original-root","parent_run_id":"original-root","retry_of":"previous-child","retrier":{"set":true,"value":{"kind":"account","id":"bob"}},"admission_subject":{"kind":"account","id":"bob"}}`))
	rewriteSchemaExec(t, s.db, `UPDATE teams SET next_run_number=3 WHERE name='a'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET created_at=? WHERE team='a'`, s.now().UnixNano())
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{ComputeLimitRunsPerHour: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := tenant.AdmitRun(t.Context(), request)
	var limit *ComputeLimitError
	if !errors.As(err, &limit) || limit.Principal != "bob" {
		t.Fatalf("child did not spend retrier's exhausted hourly budget: %v", err)
	}
}
