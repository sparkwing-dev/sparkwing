package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithRunner returns ctx with r installed. The orchestrator calls
// this per dispatched job before invoking the job body; tests use
// it to construct a ctx for adapter code that reads Runner(ctx).
//
// Nil r is honored: the resulting ctx surfaces Runner(ctx) = nil,
// matching the no-install default.
func WithRunner(ctx context.Context, r *sparkwing.RunnerInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Runner, r)
}
