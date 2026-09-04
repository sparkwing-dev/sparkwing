package orchestrator

import (
	"context"
	"log/slog"
	"os/exec"
	"time"
)

const assistedChildCleanupRetryInitial = 50 * time.Millisecond

const assistedChildCleanupRetryMax = time.Second

type assistedChildProcess interface {
	exited() <-chan struct{}
	finish(*slog.Logger) error
	terminate(*slog.Logger) error
}

type assistedChildOutcome struct {
	waitErr     error
	cancelCause error
}

type failedAssistedChildCleanup struct {
	inspect       func() (bool, error)
	terminate     func() error
	wait          func()
	sleep         func(time.Duration)
	processID     int
	boundary      string
	inspectAction string
	stopAction    string
}

func (c failedAssistedChildCleanup) settle(logger *slog.Logger) {
	retry := assistedChildCleanupRetryInitial
	terminated := false
	for {
		empty, err := c.inspect()
		if err != nil {
			logger.Error("assisted child failed-start cleanup retained ownership",
				"process_id", c.processID,
				"ownership_boundary", c.boundary,
				"operation", c.inspectAction,
				"err", err,
				"retry_in", retry)
		} else if empty {
			c.wait()
			return
		} else if !terminated {
			err = c.terminate()
			if err == nil {
				terminated = true
			} else {
				logger.Error("assisted child failed-start cleanup retained ownership",
					"process_id", c.processID,
					"ownership_boundary", c.boundary,
					"operation", c.stopAction,
					"err", err,
					"retry_in", retry)
			}
		}
		c.sleep(retry)
		retry = nextAssistedChildCleanupRetry(retry)
	}
}

func nextAssistedChildCleanupRetry(current time.Duration) time.Duration {
	if current >= assistedChildCleanupRetryMax {
		return assistedChildCleanupRetryMax
	}
	next := current * 2
	if next > assistedChildCleanupRetryMax {
		return assistedChildCleanupRetryMax
	}
	return next
}

func runAssistedChildProcess(
	ctx context.Context,
	cmd *exec.Cmd,
	logger *slog.Logger,
) (assistedChildOutcome, error) {
	if err := ctx.Err(); err != nil {
		return assistedChildOutcome{cancelCause: context.Cause(ctx)}, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	child, err := startAssistedChildProcess(cmd, logger)
	if err != nil {
		return assistedChildOutcome{}, err
	}

	var cancelCause error
	select {
	case <-child.exited():
	default:
		select {
		case <-child.exited():
		case <-ctx.Done():
			cancelCause = context.Cause(ctx)
		}
	}

	if cancelCause != nil {
		return assistedChildOutcome{
			waitErr:     child.terminate(logger),
			cancelCause: cancelCause,
		}, nil
	}
	// safety: a successful leader cannot release its helper slot while descendants from its body remain live.
	return assistedChildOutcome{waitErr: child.finish(logger)}, nil
}
