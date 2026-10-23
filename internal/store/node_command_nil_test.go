package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestTransitionNodeRejectsMissingCommand(t *testing.T) {
	var tenant Tenant
	for _, command := range []NodeCommand{nil, (*StartNode)(nil), (*ReportNode)(nil), (*nodeTransition)(nil)} {
		ref, actor := NodeCommandAuthority(command)
		if ref != (runspec.NodeRef{}) || actor != (runstate.Actor{}) {
			t.Fatal("missing command retained authority")
		}
		if bound := BindNodeCommand(command, runspec.NodeRef{RunID: "run"}, runstate.Actor{Kind: runstate.ActorHolder}); bound != nil {
			t.Fatal("missing command acquired a binding")
		}
		_, err := tenant.TransitionNode(t.Context(), command)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.BadRequest {
			t.Fatalf("%T: want bad_request, got %v", command, err)
		}
	}
}
