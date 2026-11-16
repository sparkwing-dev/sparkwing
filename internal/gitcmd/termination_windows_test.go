//go:build windows

package gitcmd

import (
	"context"
	"os/exec"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/processtest"
)

func TestWindowsTerminationChild(t *testing.T) { processtest.WindowsChild(t) }

func TestWindowsOwnerTermination(t *testing.T) {
	processtest.CheckWindowsTermination(t, true, func(ctx context.Context, cmd *exec.Cmd) error { _, err := Output(ctx, cmd, false); return err })
}
