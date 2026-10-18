package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func childIntakeFixture(t *testing.T) (*Tenant, IntakeRequest) {
	t.Helper()
	tenant, _, actor := liveHolderFixture(t)
	parent := authorityRoot("child-parent", "a")
	parent.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: actor.Holder, Generation: actor.Generation}
	parent.Grants = []capability.Grant{capability.ChildrenStart}
	authorityPersist(t, tenant.s, parent)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET capability_prefix='child-parent'`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	tenant.s.intake = fakeIntakeContracts{}
	tenant.s.SetIntakePolicy(fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	}))
	return tenant, IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}, IdempotencyKey: "invocation", Request: runspec.Request{Version: 1, Team: "a", Pipeline: "child", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Lineage: runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}}}
}

func TestChildIntakePersistsLineageAndInvocationReplay(t *testing.T) {
	tenant, request := childIntakeFixture(t)
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || first.Replayed || first.OwnerUnit == nil || first.Request.Request.Origin != runspec.OriginChild || first.Request.Request.Lineage != request.Request.Lineage {
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
