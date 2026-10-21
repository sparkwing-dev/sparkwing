package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestNodeCommandPayloadRejectsConflictingPayload(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	actor := Actor{Kind: ActorHolder}
	commands := []NodeCommand{
		StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))},
		RetryNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Result: &NodeResult{Outcome: NodeFailed}},
		ReportNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Outcome: NodeSuccess},
		ReadyNode{Ref: ref, Actor: actor},
		ReleaseNode{Ref: ref, Actor: actor},
		DependencyNode{Ref: ref, Actor: actor, Outcome: NodeSkipped},
		OwnerPolicyNode{Ref: ref, Actor: actor, Outcome: NodeFailed, OwnerPolicy: &OwnerPolicyResult{}},
		OpenApprovalNode{Ref: ref, Actor: actor},
		ResolveApprovalNode{Ref: ref, Actor: actor, Outcome: NodeSuccess},
		materializeNode{Ref: ref, Actor: actor},
		publishNode{Ref: ref, Actor: actor, Outcome: NodeSuccess},
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
		rebuilt, err := body.NodeCommand(ref, actor)
		if err != nil {
			t.Fatalf("%T round trip: %v", command, err)
		}
		if gotRef, gotActor := NodeCommandAuthority(rebuilt); gotRef != ref || gotActor.Kind != actor.Kind {
			t.Fatal("transport changed bound authority")
		}
		if body.Cause == causeMaterialize {
			body.Extra.Result = &NodeResult{}
		} else {
			body.Extra.Materialize = &MaterializeSource{}
		}
		if _, err := body.NodeCommand(ref, actor); err == nil {
			t.Fatalf("%T accepted another command's payload", command)
		}
	}
	body, _ := NodeCommandPayloadFor(StartNode{SourceAttempt: new(runspec.AttemptOrdinal(0))})
	body.To = NodeSuccess
	if _, err := body.NodeCommand(ref, actor); err == nil {
		t.Fatal("start accepted a terminal destination")
	}
	body, _ = NodeCommandPayloadFor(CancelNode{})
	body.Extra.RetryAt = new(time.Time)
	if _, err := body.NodeCommand(ref, actor); err == nil {
		t.Fatal("cancel accepted retry scheduling")
	}
}
