package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

var _ func(*NodeExecutor, context.Context, runner.Request, coordParams, string, string) runner.Result = (*NodeExecutor).applyCacheHit

var _ func(*NodeExecutor, context.Context, string, string) ([]byte, error) = (*NodeExecutor).fetchCachedOutput
