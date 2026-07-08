package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithSpawnHandler installs h into ctx. The orchestrator wraps the
// per-node ctx with this before calling RunWork.
func WithSpawnHandler(ctx context.Context, h sparkwing.SpawnHandler) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.SpawnHandler, h)
}

// SpawnHandlerFromContext returns the installed handler or nil.
// RunWork errors loudly if a Work declares spawns and no handler is
// present.
func SpawnHandlerFromContext(ctx context.Context) sparkwing.SpawnHandler {
	if h, ok := ctx.Value(sparkwing.RuntimePlumbing.Keys.SpawnHandler).(sparkwing.SpawnHandler); ok {
		return h
	}
	return nil
}
