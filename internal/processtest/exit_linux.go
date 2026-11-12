//go:build linux

package processtest

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// WaitExited observes death without taking the status away from its owner.
func WaitExited(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		// safety: ECHILD means the command's owner already consumed the dead child's status.
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		return err
	}
}

// KilledContext records fixture failures separately from the command's wait result.
type KilledContext struct {
	context.Context
	killErr error
}

// KillBeforeCancel makes external death observable before the owner receives cancellation.
func KillBeforeCancel(ctx context.Context, cmd *exec.Cmd, cancel context.CancelFunc) *KilledContext {
	c := &KilledContext{}
	c.Context = BeforeDone(ctx, func() {
		c.killErr = cmd.Process.Kill()
		if c.killErr == nil {
			c.killErr = WaitExited(cmd.Process.Pid)
		}
		cancel()
	})
	return c
}

// KillError reports whether the fixture established the external-death ordering.
func (c *KilledContext) KillError() error { return c.killErr }
