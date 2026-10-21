package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type capturedNodeCommand struct {
	Command store.NodeCommand
	Ref     controllerwire.NodeRef
	Actor   store.Actor
	store.NodeCommandPayload
	Extra capturedCommandExtra
}
type capturedCommandExtra struct {
	store.NodeCommandExtra
	Attempt *store.NodeAttempt
}

func captureNodeCommand(command store.NodeCommand) capturedNodeCommand {
	ref, actor := store.NodeCommandAuthority(command)
	body, err := store.NodeCommandPayloadFor(command)
	if err != nil {
		panic(err)
	}
	out := capturedNodeCommand{Command: command, Ref: ref, Actor: actor, NodeCommandPayload: body, Extra: capturedCommandExtra{NodeCommandExtra: body.Extra}}
	if start, ok := command.(store.StartNode); ok {
		out.Extra.Attempt = start.Attempt
	}
	return out
}
