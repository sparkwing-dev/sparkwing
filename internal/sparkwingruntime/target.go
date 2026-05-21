package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithTarget returns a derived context carrying the active target.
// Used by the orchestrator at run start to publish the resolved
// --for selection; tests use it to exercise target-conditional code
// paths from a bare ctx.
func WithTarget(ctx context.Context, target string) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Target, target)
}
