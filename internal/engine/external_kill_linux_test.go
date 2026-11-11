//go:build linux

package engine

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

type externalKillContext struct {
	context.Context
	command *exec.Cmd
	cancel  context.CancelFunc
	once    sync.Once
	killErr error
}

func (c *externalKillContext) Done() <-chan struct{} {
	c.once.Do(func() {
		c.killErr = c.command.Process.Kill()
		if c.killErr == nil {
			var info unix.Siginfo
			for {
				c.killErr = unix.Waitid(unix.P_PID, c.command.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
				if !errors.Is(c.killErr, syscall.EINTR) {
					break
				}
			}
		}
		c.cancel()
	})
	return c.Context.Done()
}

func TestExternalSIGKILLRemainsFailure(t *testing.T) {
	erased, retained := 0, 0
	for i := 0; i < 24; i++ {
		base, cancel := context.WithCancel(t.Context())
		command := exec.Command("/bin/sleep", "30")
		ctx := &externalKillContext{Context: base, command: command, cancel: cancel}
		err := runSDKInstallProcess(ctx, command)
		cancel()
		if ctx.killErr != nil {
			t.Fatalf("external kill/wait failed: %v", ctx.killErr)
		}
		var exit *exec.ExitError
		if errors.Is(err, context.Canceled) && !errors.As(err, &exit) {
			erased++
		} else {
			if !errors.As(err, &exit) {
				t.Fatalf("unexpected result: %v", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("not an external SIGKILL: %v", err)
			}
			retained++
		}
	}
	t.Logf("external SIGKILL confirmed before cancellation: erased=%d retained=%d", erased, retained)
	if erased != 0 {
		t.Fatalf("genuine externally issued SIGKILL became cancellation in %d/24 trials", erased)
	}
}
