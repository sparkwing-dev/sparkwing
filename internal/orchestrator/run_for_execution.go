package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type executionRunGetter interface {
	GetRunForExecution(ctx context.Context, runID string) (*store.Run, error)
}

func runForExecution(ctx context.Context, state StateBackend, runID string) (*store.Run, error) {
	if ex, ok := state.(executionRunGetter); ok {
		return ex.GetRunForExecution(ctx, runID)
	}
	return state.GetRun(ctx, runID)
}
