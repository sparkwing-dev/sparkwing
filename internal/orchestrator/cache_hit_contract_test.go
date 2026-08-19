package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

var _ func(*InProcessRunner, context.Context, runner.Request, coordParams, string, string) runner.Result = (*InProcessRunner).applyCacheHit

var _ func(*InProcessRunner, context.Context, string, string) ([]byte, error) = (*InProcessRunner).fetchCachedOutput
