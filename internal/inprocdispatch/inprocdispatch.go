package inprocdispatch

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type InProcessDispatcher struct {
	Backends orchestrator.Backends
	Logger   *slog.Logger

	MaxParallel int
}

func (d InProcessDispatcher) Dispatch(ctx context.Context, req controller.RunRequest) error {
	lg := d.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info(
		"in-process dispatch",
		"run_id", req.RunID,
		"pipeline", req.Pipeline,
		"trigger", req.Trigger.Source,
	)

	go func() {
		runCtx := context.Background()
		res, err := orchestrator.Run(runCtx, d.Backends, orchestrator.Options{
			Pipeline:    req.Pipeline,
			RunID:       req.RunID,
			Args:        req.Args,
			Trigger:     req.Trigger,
			Git:         req.Git,
			ParentRunID: req.ParentRunID,
			RetryOf:     req.RetryOf,
			MaxParallel: d.MaxParallel,
		})
		if err != nil {
			lg.Error(
				"dispatched run failed",
				"run_id", req.RunID,
				"pipeline", req.Pipeline,
				"err", err,
			)
			return
		}
		lg.Info(
			"dispatched run finished",
			"run_id", res.RunID,
			"pipeline", req.Pipeline,
			"status", res.Status,
		)
	}()
	return nil
}
