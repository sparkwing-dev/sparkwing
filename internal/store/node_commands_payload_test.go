package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
