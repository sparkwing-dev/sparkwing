//go:build !windows

package engine

import (
	"context"
	"errors"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runSDKInstallProcess(ctx context.Context, command *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command.WaitDelay = procgroup.DefaultTerminationGrace
	group, err := procgroup.StartSession(command)
	if err != nil {
		return err
	}
	err = group.Finish(ctx, procgroup.DefaultTerminationGrace)
	if group.Reaped() {
		return err
	}
	killErr := group.Kill()
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*procgroup.DefaultTerminationGrace)
	defer cancel()
	finishErr := group.Finish(cleanup, procgroup.DefaultTerminationGrace)
	if !group.Reaped() {
		return errors.Join(err, killErr, finishErr, command.Process.Kill(), command.Wait())
	}
	return errors.Join(err, killErr, finishErr)
}
