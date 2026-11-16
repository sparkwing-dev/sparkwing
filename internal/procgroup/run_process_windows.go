//go:build windows

package procgroup

import (
	"context"
	"os/exec"

	"golang.org/x/sys/windows"
)

// RunProcess owns leader cancellation without changing descendant policy.
func RunProcess(ctx context.Context, cmd *exec.Cmd) error {
	return runOwnedCommand(ctx, cmd, func() error { return cmd.Start() }, func(process windows.Handle) error {
		// safety: zero is startup-failure cleanup before a retained handle is available.
		if process == 0 {
			return cmd.Process.Kill()
		}
		return windows.TerminateProcess(process, ownerTerminationCode)
	}, nil)
}
