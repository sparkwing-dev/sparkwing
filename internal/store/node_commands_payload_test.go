package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestNodeCommandPayloadRejectsConflictingPayload(t *testing.T) {
	ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
	actor := runstate.Actor{Kind: runstate.ActorHolder}
	commands := []NodeCommand{
		StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))},
		RetryNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}},
		ReportNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Outcome: runstate.NodeSuccess},
		ReadyNode{Ref: ref, Actor: actor},
		ReleaseNode{Ref: ref, Actor: actor},
		DependencyNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSkipped},
		OwnerPolicyNode{Ref: ref, Actor: actor, Outcome: runstate.NodeFailed, OwnerPolicy: &runstate.OwnerPolicyResult{}},
		OpenApprovalNode{Ref: ref, Actor: actor},
		ResolveApprovalNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSuccess},
		materializeNode{Ref: ref, Actor: actor},
		publishNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSuccess},
		publishNodeAttempt{Ref: ref, Actor: actor},
		RevokeNode{Ref: ref, Actor: actor},
		CancelNode{Ref: ref, Actor: actor},
		TimeoutApprovalNode{Ref: ref, Actor: actor},
	}
	for _, command := range commands {
		body, err := NodeCommandPayloadFor(command)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt, err := NodeCommandFromPayload(body, ref, actor)
		if err != nil {
			t.Fatalf("%T round trip: %v", command, err)
		}
		if gotRef, gotActor := NodeCommandAuthority(rebuilt); gotRef != ref || gotActor.Kind != actor.Kind {
			t.Fatal("transport changed bound authority")
		}
		if body.Cause == causeMaterialize {
			body.Extra.Result = &runstate.NodeResult{}
		} else {
			body.Extra.Materialize = &runstate.MaterializeSource{}
		}
		if _, err := NodeCommandFromPayload(body, ref, actor); err == nil {
			t.Fatalf("%T accepted another command's payload", command)
		}
	}
	body, _ := NodeCommandPayloadFor(StartNode{SourceAttempt: new(runspec.AttemptOrdinal(0))})
	body.To = runstate.NodeSuccess
	if _, err := NodeCommandFromPayload(body, ref, actor); err == nil {
		t.Fatal("start accepted a terminal destination")
	}
	body, _ = NodeCommandPayloadFor(CancelNode{})
	body.Extra.RetryAt = new(time.Time)
	if _, err := NodeCommandFromPayload(body, ref, actor); err == nil {
		t.Fatal("cancel accepted retry scheduling")
	}
}

func TestAdmissionRefusalHasNoOwnerCommand(t *testing.T) {
	_, err := NodeCommandFromPayload(runstate.NodeCommandPayload{Cause: causeAdmissionRefused, To: runstate.NodeSkipped}, runspec.NodeRef{RunID: "run", NodeID: "job"}, runstate.Actor{Kind: runstate.ActorRunHost})
	if err == nil {
		t.Fatal("store admission refusal accepted as an owner command")
	}
}

func TestOwnerCannotSkipApprovalThroughDependencyEdge(t *testing.T) {
	tenant, plan, _ := planFixture(t, runspec.DispatchHost)
	doc := testBodyPlan(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}}}})
	plan.Seal.Plan, _ = json.Marshal(doc)
	var err error
	plan.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: plan.Seal.Request, Declarations: plan.Seal.Declarations, Plan: doc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "gate"}
	for _, outcome := range []runstate.NodeState{runstate.NodeSkipped, runstate.NodeCancelled} {
		if _, err := tenant.TransitionNode(t.Context(), DependencyNode{Ref: ref, Actor: plan.Actor, Outcome: outcome}); err == nil {
			t.Errorf("owner forged dependency outcome %s", outcome)
		}
	}
	if _, err := tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Cause: runstate.RunFinish, Actor: plan.Actor}}); err == nil {
		t.Error("owner finished an unresolved approval green")
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Status != runstate.StatusPending || row.Outcome != "" {
		t.Fatalf("refused dependency verdict changed approval: %+v %v", row, err)
	}
}

func TestDependencySkipUsesSealedSelection(t *testing.T) {
	for _, selected := range []runspec.NodeID{"build", "gate"} {
		t.Run(string(selected), func(t *testing.T) {
			tenant, plan, _ := planFixtureWithInput(t, runspec.DispatchHost, func(in *IntakeRequest) {
				in.Request.Options.Only = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: string(selected)}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag}}
			})
			doc := testBodyPlan(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build"}, {ID: "gate", Approval: &nodeproto.ApprovalSpec{}}}})
			plan.Seal.Plan, _ = json.Marshal(doc)
			var err error
			plan.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: plan.Seal.Request, Declarations: plan.Seal.Declarations, Plan: doc})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			snapshot, err := tenant.ReadSnapshot(t.Context(), plan.RunID)
			if err != nil {
				t.Fatal(err)
			}
			graph, err := snapshot.Scheduling()
			if err != nil {
				t.Fatal(err)
			}
			decision := Decide(graph)
			if len(decision.Nodes) != 1 || decision.Nodes[0].NodeID == string(selected) || decision.Nodes[0].Kind != DecisionTerminal || decision.Nodes[0].Outcome != "skipped" || decision.Nodes[0].Reason != `outside --only="`+string(selected)+`"` {
				t.Fatalf("store did not decide the sealed selection: %+v", decision)
			}
			for _, id := range []runspec.NodeID{"build", "gate"} {
				ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: id}
				_, err := tenant.TransitionNode(t.Context(), DependencyNode{Ref: ref, Actor: plan.Actor, Outcome: runstate.NodeSkipped})
				if (err == nil) != (id != selected) {
					t.Fatalf("dependency skip %s: %v", id, err)
				}
			}
		})
	}
}
