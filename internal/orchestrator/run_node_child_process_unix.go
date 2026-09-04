//go:build !windows

package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const assistedChildTerminationGrace = time.Second

const assistedChildOwnershipBoundary = "process_session"

type unixAssistedChildProcess struct {
	group *procgroup.Group
}

func startAssistedChildProcess(cmd *exec.Cmd, _ *slog.Logger) (assistedChildProcess, error) {
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		return nil, err
	}
	return &unixAssistedChildProcess{group: group}, nil
}

func (p *unixAssistedChildProcess) exited() <-chan struct{} {
	return p.group.LeaderExited()
}

func (p *unixAssistedChildProcess) finish(logger *slog.Logger) error {
	return p.settle(false, logger)
}

func (p *unixAssistedChildProcess) terminate(logger *slog.Logger) error {
	return p.settle(true, logger)
}

func (p *unixAssistedChildProcess) settle(terminate bool, logger *slog.Logger) error {
	// safety: returning on inspection failure could release helper capacity while its process tree remains live.
	retry := assistedChildCleanupRetryInitial
	for {
		var err error
		if terminate {
			err = p.group.Terminate(context.Background(), assistedChildTerminationGrace)
		} else {
			err = p.group.Finish(context.Background(), assistedChildTerminationGrace)
		}
		if !errors.Is(err, procgroup.ErrCleanup) {
			return err
		}

		logger.Error("assisted child process session cleanup failed; retaining ownership",
			"process_id", p.group.ID(), "err", err, "retry_in", retry)
		time.Sleep(retry)
		terminate = true
		retry = nextAssistedChildCleanupRetry(retry)
	}
}
