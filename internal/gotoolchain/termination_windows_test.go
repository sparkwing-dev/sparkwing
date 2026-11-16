//go:build windows

package gotoolchain

import (
	"context"
	"os/exec"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/processtest"
)

func TestWindowsTerminationChild(t *testing.T) { processtest.WindowsChild(t) }

func TestRunWindowsAttributesTermination(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		processtest.CheckWindowsTermination(t, false, func(ctx context.Context, cmd *exec.Cmd) error {
			cmd.WaitDelay = 0
			return Run(ctx, cmd)
		})
	})
	t.Run("command-context", func(t *testing.T) { processtest.CheckWindowsTermination(t, true, Run) })
}
