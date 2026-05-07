package local

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RunRequest is the payload handed to a Dispatcher when a trigger
// arrives. The controller pre-assigns RunID so the HTTP caller can
// learn it immediately, even when Dispatch runs async.
type RunRequest struct {
	RunID       string
	Pipeline    string
	Args        map[string]string
	Trigger     sparkwing.TriggerInfo
	Git         *sparkwing.Git
	ParentRunID string
	RetryOf     string
}

// Dispatcher decides what happens to a triggered run. Implementations
// must return quickly -- real work goes in a goroutine or queue, not
// the HTTP handler.
type Dispatcher interface {
	Dispatch(ctx context.Context, req RunRequest) error
}

// NoopDispatcher records triggers to the logger and does nothing else.
// The right choice when the controller is purely a state backend with
// workers polling for pending runs.
type NoopDispatcher struct {
	Logger *slog.Logger
}

func (n NoopDispatcher) Dispatch(_ context.Context, req RunRequest) error {
	lg := n.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info("noop dispatch",
		"run_id", req.RunID,
		"pipeline", req.Pipeline,
		"trigger", req.Trigger.Source,
	)
	return nil
}

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

func (d InProcessDispatcher) Dispatch(ctx context.Context, req RunRequest) error {
	lg := d.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info("in-process dispatch",
		"run_id", req.RunID,
		"pipeline", req.Pipeline,
		"trigger", req.Trigger.Source,
	)

	// Detach from the HTTP request's ctx so the pipeline run outlives
	// the handler return.
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
