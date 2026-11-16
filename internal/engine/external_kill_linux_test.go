//go:build linux

package engine

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/processtest"
)

func TestExternalSIGKILLRemainsFailure(t *testing.T) {
	erased, retained := 0, 0
	for i := 0; i < 24; i++ {
		base, cancel := context.WithCancel(t.Context())
		command := exec.Command("/bin/sleep", "30")
		ctx := processtest.KillBeforeCancel(base, command, cancel)
		err := runSDKInstallProcess(ctx, command)
		cancel()
		if ctx.KillError() != nil {
			t.Fatalf("external kill/wait failed: %v", ctx.KillError())
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
