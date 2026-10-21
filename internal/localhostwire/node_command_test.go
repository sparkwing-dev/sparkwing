package localhostwire

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestLocalNodeCommandRejectsConflictingPayload(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	actor := store.Actor{Kind: store.ActorHolder}
	commands := []store.NodeCommand{
		store.StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))},
		store.RetryNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Result: &store.NodeResult{Outcome: store.NodeFailed}},
		store.ReportNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(1)), Outcome: store.NodeSuccess},
		store.ReadyNode{Ref: ref, Actor: actor},
		store.CancelNode{Ref: ref, Actor: actor},
		store.TimeoutApprovalNode{Ref: ref, Actor: actor},
	}
	for _, command := range commands {
		body, err := LocalNodeCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt, err := body.NodeCommand(ref, actor)
		if err != nil {
			t.Fatalf("%T round trip: %v", command, err)
		}
		if gotRef, gotActor := store.NodeCommandAuthority(rebuilt); gotRef != ref || gotActor.Kind != actor.Kind {
			t.Fatal("transport changed bound authority")
		}
		body.Extra.Materialize = &store.MaterializeSource{}
		if _, err := body.NodeCommand(ref, actor); err == nil {
			t.Fatalf("%T accepted another command's payload", command)
		}
	}
	body, _ := LocalNodeCommand(store.StartNode{SourceAttempt: new(runspec.AttemptOrdinal(0))})
	body.To = store.NodeSuccess
	if _, err := body.NodeCommand(ref, actor); err == nil {
		t.Fatal("start accepted a terminal destination")
	}
	body, _ = LocalNodeCommand(store.CancelNode{})
	body.Extra.RetryAt = new(time.Time)
	if _, err := body.NodeCommand(ref, actor); err == nil {
		t.Fatal("cancel accepted retry scheduling")
	}
}
