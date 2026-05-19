// Package inprocdispatch carries the in-process implementation of
// pkg/controller.Dispatcher. It lives outside pkg/controller so the
// controller package's public API stays free of orchestrator-defined
// types -- only tests, demos, and inside-module consumers (which can
// import internal/) reach for the in-process dispatcher.
//
// Production wiring (the controller pod and pkg/localws) does not use
// this dispatcher; the controller pod defaults to NoopDispatcher and
// pkg/localws drives runs through orchestrator.RunLocalTriggerConsumer.
// In-process dispatch exists primarily for full-loop tests that want
// to exercise the trigger path without a separate runner process.
package inprocdispatch

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// InProcessDispatcher runs the pipeline in a goroutine within the
// controller process. State writes go back to the same controller via
// the supplied Backends. Not intended for prod cluster-mode: it
// couples pipeline execution to the controller's process lifetime
// and offers no isolation.
type InProcessDispatcher struct {
	Backends orchestrator.Backends
	Logger   *slog.Logger
	// MaxParallel caps concurrent node execution per dispatched run.
	// Zero = unbounded.
	MaxParallel int
}

// Dispatch satisfies controller.Dispatcher. Returns once the run has
// been scheduled; the run itself executes on a detached goroutine
// that outlives the calling HTTP request.
func (d InProcessDispatcher) Dispatch(ctx context.Context, req controller.RunRequest) error {
	lg := d.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info("in-process dispatch",
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
			lg.Error("dispatched run failed",
				"run_id", req.RunID,
				"pipeline", req.Pipeline,
				"err", err,
			)
			return
		}
		lg.Info("dispatched run finished",
			"run_id", res.RunID,
			"pipeline", req.Pipeline,
			"status", res.Status,
		)
	}()
	return nil
}
