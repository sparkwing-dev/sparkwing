//go:build !windows

package releaseasset

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runProbeProcess(ctx context.Context, command *exec.Cmd, cleanupTimeout time.Duration) error {
	group, err := procgroup.StartSession(command)
	if err != nil {
		return err
	}
	return finishProbeProcess(ctx, command, group, cleanupTimeout)
}

func finishProbeProcess(ctx context.Context, command *exec.Cmd, group *procgroup.Group, cleanupTimeout time.Duration) error {
	err := group.Finish(ctx, cleanupTimeout/2)
	if err == nil || group.Reaped() {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	err = errors.Join(err, group.Terminate(cleanupCtx, cleanupTimeout/2))
	if group.Reaped() {
		return err
	}
	return errors.Join(err, disposeProbeProcess(command, group.WaitLeaderExit))
}

func disposeProbeProcess(command *exec.Cmd, waitForLeader func() error) error {
	killErr := command.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	// SAFETY: Group signaling has ended. Reaping waits for OS-confirmed leader exit;
	// WaitDelay bounds inherited pipes, while kernel process exit has no deadline.
	_ = waitForLeader()
	return errors.Join(killErr, command.Wait())
}
