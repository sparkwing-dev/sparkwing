//go:build !windows

package engine

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

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
	killedLeader, killErr := group.KillForCancellation() //nolint:contextcheck // Cleanup must outlive cancellation and uses the owner's bounded process-table lookup.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*procgroup.DefaultTerminationGrace)
	defer cancel()
	finishErr := group.Finish(cleanup, procgroup.DefaultTerminationGrace)
	if !group.Reaped() {
		return errors.Join(err, killErr, finishErr, command.Process.Kill(), command.Wait())
	}
	// safety: completed cleanup makes the interrupted wait a cancellation, not a cleanup failure.
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) && interruptedSDKWait(err) {
		err = ctx.Err()
	}
	if ctx.Err() != nil && killedLeader {
		if exit, ok := finishErr.(*exec.ExitError); ok { //nolint:errorlint // Joined failures must retain every error.
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
				finishErr = ctx.Err()
			}
		}
	}
	return errors.Join(err, killErr, finishErr)
}

func interruptedSDKWait(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !interruptedSDKWait(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return interruptedSDKWait(wrapped)
	}
	return errors.Is(err, procgroup.ErrCleanup) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
