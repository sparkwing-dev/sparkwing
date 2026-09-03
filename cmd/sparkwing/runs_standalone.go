package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// safety: only the refusal is rewritten. A run absent from every store keeps
// the writer's own sentence, which already names the id it looked for.
func standaloneWriteError(ctx context.Context, paths orchestrator.Paths, runID, verb string, wrapped error) error {
	if err := orchestrator.StandaloneRunError(ctx, paths, runID, verb); err != nil {
		return err
	}
	return wrapped
}

func standaloneWriteErrorAtHome(ctx context.Context, home, runID, verb string, wrapped error) error {
	paths, err := submitPaths(home)
	if err != nil {
		return wrapped
	}
	return standaloneWriteError(ctx, paths, runID, verb, wrapped)
}
