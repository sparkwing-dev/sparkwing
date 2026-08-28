package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func resolveTriggerArgs(ctx context.Context, state StateBackend, trigger *store.Trigger, logger *slog.Logger) map[string]string {
	if trigger.RetryOf == "" {
		return trigger.Args
	}
	if state == nil {
		return trigger.Args
	}

	orig, err := runForExecution(ctx, state, trigger.RetryOf)
	if err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn(
			"retry-of: original run args not fetchable; falling back to invocation args",
			"retry_of", trigger.RetryOf,
			"err", err,
			"is_not_found", errors.Is(err, store.ErrNotFound),
		)
		return trigger.Args
	}
	return orig.Args
}
