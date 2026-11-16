//go:build windows

package gotoolchain

import (
	"context"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func runToolchain(ctx context.Context, cmd *exec.Cmd) error {
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = time.Second
	}
	return procgroup.RunProcess(ctx, cmd)
}
