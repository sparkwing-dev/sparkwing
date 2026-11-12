//go:build !windows

package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// Output runs and joins a Git process and its descendants.
func Output(ctx context.Context, cmd *exec.Cmd, combined bool) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = nil
	if combined {
		cmd.Stderr = &output
	}
	cmd.Cancel = nil
	group, err := procgroup.Start(cmd)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		killedLeader, killErr := group.KillForCancellation() //nolint:contextcheck // Cleanup must outlive cancellation and uses the owner's bounded process-table lookup.
		waitErr := group.Finish(context.WithoutCancel(ctx), 0)
		// safety: an exit from our cancellation kill is not a separate Git failure.
		if exit, ok := waitErr.(*exec.ExitError); ok && killedLeader { //nolint:errorlint // Joined failures must retain every error.
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
				waitErr = ctx.Err()
			}
		}
		return output.Bytes(), errors.Join(killErr, waitErr)
	case <-group.LeaderExited():
	}
	err = errors.Join(err, group.Finish(context.WithoutCancel(ctx), 0))
	return output.Bytes(), err
}
