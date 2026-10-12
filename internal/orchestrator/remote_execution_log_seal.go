package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
)

const childLogSealBudget = httpNodeLogFinishTimeout

// safety: a child killed by a signal or cancelled may not have written everything, so its log
// is sealed as stopped and reads as cut off.
func (b *remoteExecutionBroker) sealChildLogs(ctx context.Context, outcome assistedChildOutcome, startErr error, _ *slog.Logger) {
	if b.logSink != nil {
		b.logSink.close(ctx, startErr != nil || outcome.cancelCause != nil || !exitedOnItsOwn(outcome.waitErr))
	}
}

func exitedOnItsOwn(waitErr error) bool {
	if waitErr == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(waitErr, &exitErr) && exitErr.Exited()
}
