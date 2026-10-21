package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type capturedNodeCommand struct {
	Command store.NodeCommand
	Ref     controllerwire.NodeRef
	Actor   store.Actor
	localhostwire.LocalNodeTransition
	Extra capturedCommandExtra
}
type capturedCommandExtra struct {
	localhostwire.LocalTransitionExtra
	Attempt *store.NodeAttempt
}

func captureNodeCommand(command store.NodeCommand) capturedNodeCommand {
	ref, actor := store.NodeCommandAuthority(command)
	body, err := localhostwire.LocalNodeCommand(command)
	if err != nil {
		panic(err)
	}
	out := capturedNodeCommand{Command: command, Ref: ref, Actor: actor, LocalNodeTransition: body, Extra: capturedCommandExtra{LocalTransitionExtra: body.Extra}}
	if start, ok := command.(store.StartNode); ok {
		out.Extra.Attempt = start.Attempt
	}
	return out
}
