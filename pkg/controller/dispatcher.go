package controller

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RunRequest is the payload handed to a Dispatcher when a trigger
// arrives. The controller pre-assigns RunID so the HTTP caller can
// learn it immediately, even when Dispatch runs async.
type RunRequest struct {
	RunID                            string
	Pipeline                         string
	Args                             map[string]string
	Trigger                          sparkwing.TriggerInfo
	Git                              *sparkwing.Git
	ParentRunID                      string
	RetryOf                          string
	InheritedPlanConcurrencyKey      string
	InheritedPlanConcurrencyHolderID string
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
	lg.Info(
		"noop dispatch",
		"run_id", req.RunID,
		"pipeline", req.Pipeline,
		"trigger", req.Trigger.Source,
	)
	return nil
}
