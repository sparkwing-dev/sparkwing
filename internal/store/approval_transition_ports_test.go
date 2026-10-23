package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestApprovalTransitionsPersistDecisionsAndRefuseOverwrite(t *testing.T) {
	for _, decision := range []string{"approved", "denied"} {
		t.Run(decision, func(t *testing.T) {
			tenant, ref, _, _, change := approvalReaperFixture(t, "fail")
			before, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || before.ResolvedAt != nil {
				t.Fatalf("pending gate: %+v, %v", before, err)
			}
			change.Extra.Approval = &runstate.ApprovalResolution{Decision: decision, Comment: "decision comment"}
			if decision == "approved" {
				change.To = runstate.NodeSuccess
			}
			foreign := &Tenant{s: tenant.s, team: "b"}
			if _, err := foreign.TransitionNode(t.Context(), change); err == nil {
				t.Fatal("foreign tenant resolved approval")
			}
			if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			got, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || got.Resolution != decision || got.Comment != "decision comment" || got.Approver != "approver" || got.ResolvedAt == nil {
				t.Fatalf("resolved gate: %+v, %v", got, err)
			}
			pending, err := tenant.ListPendingApprovals(t.Context())
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending after resolution: %+v, %v", pending, err)
			}
			all, err := tenant.ListApprovalsForRun(t.Context(), string(ref.RunID))
			if err != nil || len(all) != 1 || !reflect.DeepEqual(got, all[0]) {
				t.Fatalf("approval history: %+v, %v", all, err)
			}
			replay, err := tenant.TransitionNode(t.Context(), change)
			if err != nil || !replay.Replayed {
				t.Fatalf("decision replay: %+v, %v", replay, err)
			}
			change.To = runstate.NodeFailed
			change.Extra.Approval = &runstate.ApprovalResolution{Decision: "denied", Comment: "replacement"}
			if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
				t.Fatal("changed decision overwrote approval")
			}
			after, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || !reflect.DeepEqual(got, after) {
				t.Fatal("refused decision changed approval")
			}
			if _, err := foreign.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID)); err == nil {
				t.Fatal("foreign tenant read approval")
			}
			change.Ref.NodeID = "missing"
			if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
				t.Fatal("missing approval resolved")
			}
		})
	}
}

func TestApprovalTransitionsKeepPendingRequestOrder(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchHost)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "b", Approval: &nodeproto.ApprovalSpec{Message: "second"}},
		{ID: "a", Approval: &nodeproto.ApprovalSpec{Message: "first"}},
		{ID: "c", Approval: &nodeproto.ApprovalSpec{Message: "third"}},
	}}
	raw, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Plan = raw
	request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: request.Seal.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		*now = now.Add(time.Second)
		if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: runspec.NodeID(id)}, Actor: request.Actor, To: runstate.NodeApprovalPending, Cause: runstate.CauseApprovalOpen}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := tenant.ListPendingApprovals(t.Context())
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending=%+v, %v", pending, err)
	}
	for i, id := range []string{"a", "b", "c"} {
		if pending[i].NodeID != id {
			t.Fatalf("pending request %d=%q want %q", i, pending[i].NodeID, id)
		}
	}
}
