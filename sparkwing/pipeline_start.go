package sparkwing

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// StartChild submits a same-repository, same-revision child and returns its run ID
// without awaiting completion. The child retains the parent's repository identity and provenance
// and continues after the parent job ends or is cancelled. Declare the target
// with StartsChildren before calling it:
//
// run, err := sparkwing.StartChild(ctx, "scan", map[string]string{"target": "staging"})
func StartChild(ctx context.Context, pipeline string, args map[string]string) (string, error) {
	starter, ok := sdkserve.ServicesFrom(ctx).(interface {
		StartChild(context.Context, sdkserve.ChildRequest) (string, error)
	})
	if !ok {
		return "", errors.New("sparkwing: child start service unavailable")
	}
	return starter.StartChild(ctx, sdkserve.ChildRequest{Pipeline: pipeline, Args: args, Detached: true})
}
