//go:build !windows

package releaseasset

import (
	"context"
	"errors"
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
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if cleanupErr := group.Terminate(cleanupCtx, cleanupTimeout/2); cleanupErr != nil {
		return errors.Join(err, cleanupErr)
	}
	return err
}
